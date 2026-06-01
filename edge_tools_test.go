package forgemcp

import (
	"context"
	"testing"

	"smeldr.dev/core"
)

// childOrder returns the ordered child IDs of parentID, read directly from the
// edge store (the composition tools have no list-children tool in this slice).
func childOrder(t *testing.T, db smeldr.DB, parentID string) []string {
	t.Helper()
	edges, err := smeldr.NewContentEdgeStore(db).Children(context.Background(), parentID)
	if err != nil {
		t.Fatalf("Children: %v", err)
	}
	ids := make([]string, len(edges))
	for i, e := range edges {
		ids[i] = e.ChildID
	}
	return ids
}

func TestEdgeTools_AddReorderRemoveSections(t *testing.T) {
	srv, db := newBlocksServer(t)
	ed := blkEditorCtx()

	page := createNode(t, srv, "page", nil)
	b1 := createNode(t, srv, "content_block", map[string]any{"title": "1"})
	b2 := createNode(t, srv, "content_block", map[string]any{"title": "2"})
	b3 := createNode(t, srv, "content_block", map[string]any{"title": "3"})

	for _, b := range []string{b1, b2, b3} {
		if _, rpcErr := callTool(t, srv, ed, "add_section", map[string]any{"parent_id": page, "child_id": b}); rpcErr != nil {
			t.Fatalf("add_section(%s): %v", b, rpcErr.Message)
		}
	}
	if got, want := childOrder(t, db, page), []string{b1, b2, b3}; !equalStrs(got, want) {
		t.Errorf("after add, order = %v, want %v", got, want)
	}

	// reorder
	if _, rpcErr := callTool(t, srv, ed, "reorder_sections", map[string]any{
		"parent_id": page, "ordered_child_ids": []any{b3, b1, b2},
	}); rpcErr != nil {
		t.Fatalf("reorder_sections: %v", rpcErr.Message)
	}
	if got, want := childOrder(t, db, page), []string{b3, b1, b2}; !equalStrs(got, want) {
		t.Errorf("after reorder, order = %v, want %v", got, want)
	}

	// remove
	if _, rpcErr := callTool(t, srv, ed, "remove_section", map[string]any{"parent_id": page, "child_id": b1}); rpcErr != nil {
		t.Fatalf("remove_section: %v", rpcErr.Message)
	}
	if got, want := childOrder(t, db, page), []string{b3, b2}; !equalStrs(got, want) {
		t.Errorf("after remove, order = %v, want %v", got, want)
	}
}

func TestEdgeTools_AddItem_RoleAndTypeDerivation(t *testing.T) {
	srv, _ := newBlocksServer(t)

	collection := createNode(t, srv, "gallery", nil)
	image := createNode(t, srv, "image", map[string]any{"media_url": "/m/x.jpg"})

	res, rpcErr := callTool(t, srv, blkEditorCtx(), "add_item", map[string]any{
		"parent_id": collection, "child_id": image,
	})
	if rpcErr != nil {
		t.Fatalf("add_item: %v", rpcErr.Message)
	}
	edge := unwrapToolResult(t, res)
	if edge["EdgeRole"] != "item" {
		t.Errorf("EdgeRole = %v, want item", edge["EdgeRole"])
	}
	// Types derived from the stored blocks, not passed by the caller.
	if edge["ParentType"] != "gallery" {
		t.Errorf("ParentType = %v, want gallery (derived)", edge["ParentType"])
	}
	if edge["ChildType"] != "image" {
		t.Errorf("ChildType = %v, want image (derived)", edge["ChildType"])
	}
}

func TestEdgeTools_RequiresEditor(t *testing.T) {
	srv, _ := newBlocksServer(t)
	page := createNode(t, srv, "page", nil)
	block := createNode(t, srv, "content_block", nil)

	// Author may create nodes but not compose them.
	_, rpcErr := callTool(t, srv, newAuthorCtx(), "add_section", map[string]any{"parent_id": page, "child_id": block})
	if rpcErr == nil {
		t.Fatal("expected forbidden for Author on add_section, got nil")
	}
	if rpcErr.Code != -32001 {
		t.Errorf("code = %d, want -32001 (forbidden)", rpcErr.Code)
	}
}

func TestEdgeTools_AddSection_MissingChild(t *testing.T) {
	srv, _ := newBlocksServer(t)
	page := createNode(t, srv, "page", nil)

	_, rpcErr := callTool(t, srv, blkEditorCtx(), "add_section", map[string]any{
		"parent_id": page, "child_id": "does-not-exist",
	})
	if rpcErr == nil {
		t.Fatal("expected error for missing child, got nil")
	}
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
