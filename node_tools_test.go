package forgemcp

import (
	"database/sql"
	"encoding/json"
	"testing"

	"smeldr.dev/core"

	_ "modernc.org/sqlite"
)

// newBlocksServer builds an in-memory SQLite-backed App with the block tables
// created and a Server with block tools enabled (WithBlocks). It returns the
// server and the DB so tests can inspect edges directly.
func newBlocksServer(t *testing.T) (*Server, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Skipf("sqlite unavailable: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	if err := smeldr.CreateBlockTables(db); err != nil {
		t.Fatalf("CreateBlockTables: %v", err)
	}
	app := smeldr.New(smeldr.Config{
		BaseURL: "http://localhost",
		Secret:  []byte("test-secret-32-bytes-xxxxxxxxxxxx"),
		DB:      db,
	})
	return New(app, WithBlocks()), db
}

// callTool marshals a tools/call request and dispatches it through the server.
func callTool(t *testing.T, srv *Server, ctx smeldr.Context, name string, args map[string]any) (any, *jsonRPCError) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return srv.handleToolsCall(ctx, raw)
}

// blkEditorCtx returns an Editor-role context for composition tools.
func blkEditorCtx() smeldr.Context {
	return smeldr.NewTestContext(smeldr.User{ID: "e1", Roles: []smeldr.Role{smeldr.Editor}})
}

// createNode is a helper that creates a block and returns its ID.
func createNode(t *testing.T, srv *Server, typeName string, fields map[string]any) string {
	t.Helper()
	args := map[string]any{"type_name": typeName}
	if fields != nil {
		args["fields"] = fields
	}
	res, rpcErr := callTool(t, srv, newAuthorCtx(), "create_node", args)
	if rpcErr != nil {
		t.Fatalf("create_node: %v", rpcErr.Message)
	}
	id, _ := unwrapToolResult(t, res)["id"].(string)
	if id == "" {
		t.Fatal("create_node returned empty id")
	}
	return id
}

func TestNodeTools_CreateGetUpdate(t *testing.T) {
	srv, _ := newBlocksServer(t)
	ctx := newAuthorCtx()

	// create
	res, rpcErr := callTool(t, srv, ctx, "create_node", map[string]any{
		"type_name": "content_block",
		"fields":    map[string]any{"title": "Hello", "body": "World"},
	})
	if rpcErr != nil {
		t.Fatalf("create_node: %v", rpcErr.Message)
	}
	created := unwrapToolResult(t, res)
	if created["type_name"] != "content_block" {
		t.Errorf("type_name = %v, want content_block", created["type_name"])
	}
	if created["status"] != "draft" {
		t.Errorf("status = %v, want draft", created["status"])
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatal("empty id")
	}

	// get
	res, rpcErr = callTool(t, srv, ctx, "get_node", map[string]any{"id": id})
	if rpcErr != nil {
		t.Fatalf("get_node: %v", rpcErr.Message)
	}
	got := unwrapToolResult(t, res)
	fields, _ := got["fields"].(map[string]any)
	if fields["title"] != "Hello" {
		t.Errorf("title = %v, want Hello", fields["title"])
	}

	// update — merge: change title, add subtitle, preserve body
	res, rpcErr = callTool(t, srv, ctx, "update_node", map[string]any{
		"id":     id,
		"fields": map[string]any{"title": "Hi", "subtitle": "New"},
	})
	if rpcErr != nil {
		t.Fatalf("update_node: %v", rpcErr.Message)
	}
	updated := unwrapToolResult(t, res)
	uf, _ := updated["fields"].(map[string]any)
	if uf["title"] != "Hi" {
		t.Errorf("title = %v, want Hi", uf["title"])
	}
	if uf["subtitle"] != "New" {
		t.Errorf("subtitle = %v, want New", uf["subtitle"])
	}
	if uf["body"] != "World" {
		t.Errorf("body = %v, want World (preserved by merge)", uf["body"])
	}
}

func TestNodeTools_PublishArchive(t *testing.T) {
	srv, _ := newBlocksServer(t)
	ctx := newAuthorCtx()
	id := createNode(t, srv, "hero", map[string]any{"headline": "x"})

	res, rpcErr := callTool(t, srv, ctx, "publish_node", map[string]any{"id": id})
	if rpcErr != nil {
		t.Fatalf("publish_node: %v", rpcErr.Message)
	}
	if unwrapToolResult(t, res)["status"] != "published" {
		t.Error("publish_node did not report published")
	}

	// Idempotent second publish.
	if _, rpcErr = callTool(t, srv, ctx, "publish_node", map[string]any{"id": id}); rpcErr != nil {
		t.Fatalf("publish_node (idempotent): %v", rpcErr.Message)
	}

	res, rpcErr = callTool(t, srv, ctx, "archive_node", map[string]any{"id": id})
	if rpcErr != nil {
		t.Fatalf("archive_node: %v", rpcErr.Message)
	}
	if unwrapToolResult(t, res)["status"] != "archived" {
		t.Error("archive_node did not report archived")
	}
}

func TestNodeTools_List(t *testing.T) {
	srv, _ := newBlocksServer(t)
	createNode(t, srv, "content_block", map[string]any{"title": "a"})
	createNode(t, srv, "content_block", map[string]any{"title": "b"})
	createNode(t, srv, "hero", map[string]any{"headline": "c"})

	// filter by type
	res, rpcErr := callTool(t, srv, newAuthorCtx(), "list_nodes", map[string]any{"type_name": "content_block"})
	if rpcErr != nil {
		t.Fatalf("list_nodes: %v", rpcErr.Message)
	}
	items, _ := unwrapToolResult(t, res)["items"].([]any)
	if len(items) != 2 {
		t.Errorf("list_nodes(content_block) = %d items, want 2", len(items))
	}

	// no filter → all
	res, rpcErr = callTool(t, srv, newAuthorCtx(), "list_nodes", map[string]any{})
	if rpcErr != nil {
		t.Fatalf("list_nodes(all): %v", rpcErr.Message)
	}
	all, _ := unwrapToolResult(t, res)["items"].([]any)
	if len(all) != 3 {
		t.Errorf("list_nodes(all) = %d items, want 3", len(all))
	}
}

func TestNodeTools_RequiresAuthor(t *testing.T) {
	srv, _ := newBlocksServer(t)
	_, rpcErr := callTool(t, srv, newTestCtx(), "create_node", map[string]any{"type_name": "hero"})
	if rpcErr == nil {
		t.Fatal("expected forbidden for guest, got nil")
	}
	if rpcErr.Code != -32001 {
		t.Errorf("code = %d, want -32001 (forbidden)", rpcErr.Code)
	}
}

func TestNodeTools_ListedOnlyWithBlocks(t *testing.T) {
	// With blocks enabled, node + composition tools appear.
	srv, _ := newBlocksServer(t)
	names := toolNames(srv.handleToolsList())
	for _, want := range []string{"create_node", "list_nodes", "add_section", "add_item"} {
		if !names[want] {
			t.Errorf("tools/list missing %q with WithBlocks", want)
		}
	}

	// Without WithBlocks, they must not appear.
	app := smeldr.New(smeldr.Config{BaseURL: "http://localhost", Secret: []byte("test-secret-32-bytes-xxxxxxxxxxxx")})
	plain := New(app)
	pn := toolNames(plain.handleToolsList())
	for _, absent := range []string{"create_node", "add_section"} {
		if pn[absent] {
			t.Errorf("tools/list unexpectedly contains %q without WithBlocks", absent)
		}
	}
}

// toolNames extracts the set of tool names from a handleToolsList result.
func toolNames(result any) map[string]bool {
	out := map[string]bool{}
	m, ok := result.(map[string]any)
	if !ok {
		return out
	}
	tools, ok := m["tools"].([]mcpTool)
	if !ok {
		return out
	}
	for _, tl := range tools {
		out[tl.Name] = true
	}
	return out
}
