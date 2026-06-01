// Package forgemcp implements an MCP (Model Context Protocol) server for Forge
// applications. It exposes content modules registered with smeldr.MCP(...) as
// MCP resources and tools, enabling AI assistants to query and manage content
// through a structured protocol.
package forgemcp

import (
	"bytes"
	"encoding/json"
	"log"

	"smeldr.dev/core"
	forgeoauth "smeldr.dev/oauth"
)

// ServerOption configures a [Server]. Use [WithSecret] to override the HMAC
// secret used for SSE bearer-token authentication.
type ServerOption func(*Server)

// WithSecret overrides the HMAC secret used to verify SSE bearer tokens.
// The secret must match Config.Secret from the smeldr.App passed to New.
// Tokens minted by [smeldr.SignToken] with a different secret will fail SSE
// verification with HTTP 401. WithSecret is only needed for secret rotation.
func WithSecret(secret []byte) ServerOption {
	return func(s *Server) { s.secret = secret }
}

// WithModule registers an additional [smeldr.MCPModule] with the MCP server.
// Use this to expose modules from external sub-packages (e.g. forge-media)
// that cannot be wired through [smeldr.App.MCPModules] directly.
//
// Example:
//
//	mediaSrv := forgemedia.Register(app, store)
//	mcpSrv := forgemcp.New(app, forgemcp.WithModule(mediaSrv))
func WithModule(m smeldr.MCPModule) ServerOption {
	return func(s *Server) { s.modules = append(s.modules, m) }
}

// WithForgeFallback enables forge bearer token authentication as a fallback
// when [WithOAuth] is also configured. When both options are set, the server
// first attempts OAuth access token validation; if the token is not found in
// the OAuth store ([forgeoauth.ErrTokenNotFound]), it falls through to forge
// bearer token validation. An expired OAuth token ([forgeoauth.ErrTokenExpired])
// is never eligible for fallback — it returns HTTP 401 immediately.
//
// Use this when forge bearer token clients (Claude Desktop, forge-cli, internal
// tooling) must continue to work alongside OAuth clients (ChatGPT, Claude.ai).
// Without WithForgeFallback, WithOAuth rejects all non-OAuth tokens.
func WithForgeFallback() ServerOption {
	return func(s *Server) { s.forgeFallback = true }
}

// WithBlocks enables the block-system MCP tools for the App's DB.
//
// It exposes two tool groups (see block-system.md, T32):
//   - Generic node lifecycle: create_node, update_node, get_node, list_nodes,
//     publish_node, archive_node — operate on DynamicNode (Author role).
//   - Composition: add_section / reorder_sections / remove_section and
//     add_item / reorder_items / remove_item — operate on content edges
//     (Editor role).
//
// The tools read and write the smeldr_dynamic_content and smeldr_content_edges
// tables; create them once at startup with [smeldr.CreateBlockTables]. WithBlocks
// reads the App's configured DB (Config.DB); if the App has no DB, the block
// tools are silently not exposed.
//
//	smeldr.CreateBlockTables(db)
//	mcpSrv := forgemcp.New(app, forgemcp.WithBlocks())
func WithBlocks() ServerOption {
	return func(s *Server) {
		if db := s.app.Config().DB; db != nil {
			s.blockRepo = smeldr.NewDynamicContentRepo(db)
			s.edgeStore = smeldr.NewContentEdgeStore(db)
		}
	}
}

// WithOAuth enables OAuth 2.1 authentication for the MCP server.
// The provided forge-oauth [forgeoauth.Server] handles authorization and token
// issuance; all MCP requests (both GET /mcp SSE and POST /mcp/message) must
// carry a valid OAuth Bearer access token.
//
// The OAuth endpoints are served at the same HTTP address as the MCP endpoints.
// [Server.Handler] mounts:
//
//	GET  /.well-known/oauth-authorization-server  (RFC 8414 — served by forge-oauth)
//	GET  /.well-known/oauth-protected-resource    (RFC 9728 — served by forge-mcp)
//	GET  /oauth/authorize
//	POST /oauth/authorize
//	POST /oauth/token
//
// Example:
//
//	store, _ := forgeoauth.NewSQLiteStore("./forge-oauth.db")
//	oauthSrv := forgeoauth.New(forgeoauth.Config{
//	    Issuer: "https://cms.example.com",
//	    VerifyBearer: func(token string) bool {
//	        _, ok := smeldr.VerifyTokenString(token, app.Secret(), app.TokenStore())
//	        return ok
//	    },
//	}, store)
//	mcpSrv := forgemcp.New(app, forgemcp.WithOAuth(oauthSrv))
func WithOAuth(oauth *forgeoauth.Server) ServerOption {
	return func(s *Server) { s.oauth = oauth }
}

// Server wraps a set of [smeldr.MCPModule] values and serves the MCP protocol
// over stdio (see [Server.ServeStdio]) or HTTP SSE (see [Server.Handler]).
type Server struct {
	app           *smeldr.App // the forge App; used for BaseURL, GeneratePreviewToken, etc.
	modules       []smeldr.MCPModule
	secret        []byte                // HMAC secret for SSE bearer-token verification
	tokenStore    *smeldr.TokenStore     // non-nil when the app has a TokenStore configured
	navTree       *smeldr.NavTree        // non-nil when the app has a NavTree configured
	webhookStore  *smeldr.WebhookStore   // non-nil when the app has Webhooks configured
	webhookPool   smeldr.WebhookJobQueue // non-nil when the app has Webhooks configured
	subscriptions *subscriptionRegistry // resource subscription fan-out registry
	oauth         *forgeoauth.Server    // non-nil when OAuth 2.1 is enabled via WithOAuth
	forgeFallback bool                  // accept forge bearer tokens as fallback when OAuth enabled

	// blockRepo and edgeStore are non-nil when WithBlocks is set; they back the
	// block-system node and composition tools.
	blockRepo *smeldr.SQLRepo[*smeldr.DynamicNode]
	edgeStore *smeldr.ContentEdgeStore
}

// New creates a Server for the given forge App, collecting all content modules
// registered with smeldr.MCP(...). The App's HMAC secret (Config.Secret) is
// inherited automatically and used for SSE bearer-token verification.
// Pass [WithSecret] to override (e.g. during secret rotation).
func New(app *smeldr.App, opts ...ServerOption) *Server {
	s := &Server{
		app:           app,
		modules:       app.MCPModules(),
		secret:        app.Secret(),
		tokenStore:    app.TokenStore(),
		navTree:       app.NavTree(),
		webhookStore:  app.WebhookStore(),
		webhookPool:   app.WebhookPool(),
		subscriptions: newSubscriptionRegistry(),
	}
	for _, o := range opts {
		o(s)
	}
	if len(s.secret) > 0 && !bytes.Equal(s.secret, app.Secret()) {
		log.Printf("forge-mcp: WithSecret value differs from app Config.Secret — " +
			"tokens minted by smeldr.SignToken will fail SSE verification")
	}
	// Bridge app-level signals to resource-subscription notifications.
	// For each delivery signal, find the module that owns the content type
	// and construct the resource URI from the prefix and item slug.
	subs := s.subscriptions
	app.AddSignalListener(func(sig smeldr.Signal, typeName string, item any) {
		for _, m := range s.modules {
			if m.MCPMeta().TypeName != typeName {
				continue
			}
			slug := slugOf(item)
			if slug == "" {
				return
			}
			uri := "forge:/" + m.MCPMeta().Prefix + "/" + slug
			subs.Notify(uri)
			return
		}
	})
	return s
}

// mcpResource is the internal representation of a single MCP resource entry.
type mcpResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType"`
}

// mcpTool is the internal representation of a single MCP tool definition.
type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// allResources iterates MCPRead modules and builds the full resource list.
// Only Published items are included — lifecycle enforcement is unconditional.
func (s *Server) allResources(ctx smeldr.Context) []mcpResource {
	var out []mcpResource
	for _, m := range s.modules {
		if !hasMCPOp(m, smeldr.MCPRead) {
			continue
		}
		items, err := m.MCPList(ctx, smeldr.Published)
		if err != nil {
			continue
		}
		prefix := m.MCPMeta().Prefix
		typeName := m.MCPMeta().TypeName
		for _, item := range items {
			slug := slugOf(item)
			if slug == "" {
				continue
			}
			out = append(out, mcpResource{
				URI:      "forge:/" + prefix + "/" + slug,
				Name:     typeName + " — " + slug,
				MimeType: "application/json",
			})
		}
	}
	return out
}

// slugOnlySchema is the JSON Schema for tools that require only a slug argument.
var slugOnlySchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"slug": map[string]any{"type": "string"},
	},
	"required": []string{"slug"},
}

// fieldToProp converts a single MCPField to its JSON Schema property object.
func fieldToProp(f smeldr.MCPField) map[string]any {
	var prop map[string]any
	switch f.Type {
	case "array":
		prop = map[string]any{
			"type":  "array",
			"items": map[string]any{"type": "string"},
		}
	case "datetime":
		// "datetime" is an internal Forge type identifier. JSON Schema
		// requires the RFC 3339 date-time format expressed as a string.
		prop = map[string]any{"type": "string", "format": "date-time"}
	default:
		prop = map[string]any{"type": f.Type}
		if f.MinLength > 0 {
			prop["minLength"] = f.MinLength
		}
		if f.MaxLength > 0 {
			prop["maxLength"] = f.MaxLength
		}
		if len(f.Enum) > 0 {
			prop["enum"] = f.Enum
		}
	}
	if desc := fieldDescription(f); desc != "" {
		prop["description"] = desc
	}
	return prop
}

// mcpToolDefs returns the tool definitions for a module that has MCPWrite.
func mcpToolDefs(m smeldr.MCPModule) []mcpTool {
	meta := m.MCPMeta()
	typeSnake := snakeCase(meta.TypeName)
	schema := m.MCPSchema()

	return []mcpTool{
		{
			Name:        "create_" + typeSnake,
			Description: "Create a new " + meta.TypeName + " content item.",
			InputSchema: inputSchema(schema),
		},
		{
			Name:        "update_" + typeSnake,
			Description: "Partially update a " + meta.TypeName + " by slug.",
			InputSchema: inputSchemaUpdate(schema),
		},
		{
			Name:        "publish_" + typeSnake,
			Description: "Publish a " + meta.TypeName + " by slug.",
			InputSchema: slugOnlySchema,
		},
		{
			Name:        "schedule_" + typeSnake,
			Description: "Schedule a " + meta.TypeName + " for future publication.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"slug":         map[string]any{"type": "string"},
					"scheduled_at": map[string]any{"type": "string", "format": "date-time"},
				},
				"required": []string{"slug", "scheduled_at"},
			},
		},
		{
			Name:        "archive_" + typeSnake,
			Description: "Archive a " + meta.TypeName + " by slug.",
			InputSchema: slugOnlySchema,
		},
	}
}

// mcpAdminReadToolDefs returns the admin tool definitions for a module that
// has MCPWrite. These tools require Editor or Admin role and bypass the
// Published-only restriction, making them suitable for content management
// dashboards and admin AI assistants.
//
// For normal modules, three tools are generated:
//   - list_{type}s — list all items; optional status filter
//   - get_{type} — fetch a single item by slug regardless of status
//   - delete_{type} — permanently delete an item by slug
//
// For [smeldr.SingleInstance] modules, the list_{type}s tool is suppressed
// because the module has at most one item — get_{type} is sufficient.
func mcpAdminReadToolDefs(m smeldr.MCPModule) []mcpTool {
	meta := m.MCPMeta()
	typeSnake := snakeCase(meta.TypeName)

	getAndDelete := []mcpTool{
		{
			Name:        "get_" + typeSnake,
			Description: "Get a single " + meta.TypeName + " by slug. Requires Editor or Admin role. Returns the item at any lifecycle status.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"slug": map[string]any{"type": "string"},
				},
				"required": []string{"slug"},
			},
		},
		{
			Name:        "delete_" + typeSnake,
			Description: "Delete a " + meta.TypeName + " permanently. Requires Editor or Admin role.",
			InputSchema: slugOnlySchema,
		},
	}

	if meta.SingleInstance {
		// SingleInstance modules have at most one item — list tool is unnecessary.
		return getAndDelete
	}

	list := mcpTool{
		Name:        "list_" + typeSnake + "s",
		Description: "List all " + meta.TypeName + " items. Requires Editor or Admin role. Returns items at any lifecycle status.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"draft", "scheduled", "published", "archived"},
					"description": "Filter by lifecycle status. Omit to return all statuses.",
				},
			},
		},
	}
	return append([]mcpTool{list}, getAndDelete...)
}

// inputSchema converts []smeldr.MCPField to a JSON Schema object suitable for
// MCP tools/list, marking Required fields in the required array.
// When a field carries a forge_format or forge_description tag, a
// "description" key is added to the property using the priority rules from
// Decision 27:
//   - Both present: forge_description + " (" + forge_format + ")"
//   - Only forge_format: "(" + forge_format + ")"
//   - Neither: no description key emitted
func inputSchema(fields []smeldr.MCPField) map[string]any {
	props := make(map[string]any, len(fields))
	var required []string
	for _, f := range fields {
		props[f.JSONName] = fieldToProp(f)
		if f.Required {
			required = append(required, f.JSONName)
		}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// inputSchemaUpdate builds an update tool schema: adds a required "slug" field
// and makes all content fields optional (partial-update semantics).
// Description hints from forge_format and forge_description tags are applied
// using the same priority rules as [inputSchema] (Decision 27).
func inputSchemaUpdate(fields []smeldr.MCPField) map[string]any {
	props := map[string]any{
		"slug": map[string]any{"type": "string"},
	}
	for _, f := range fields {
		props[f.JSONName] = fieldToProp(f)
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   []string{"slug"},
	}
}

// fieldDescription builds the JSON Schema "description" string for a field
// from its Format and Description hints (Decision 27):
//   - Both non-empty: Description + " (" + Format + ")"
//   - Only Format: "(" + Format + ")"
//   - Neither: ""
func fieldDescription(f smeldr.MCPField) string {
	switch {
	case f.Description != "" && f.Format != "":
		return f.Description + " (" + f.Format + ")"
	case f.Format != "":
		return "(" + f.Format + ")"
	default:
		return ""
	}
}

// handle dispatches a JSON-RPC request to the appropriate handler.
// Returns a jsonRPCResponse ready for serialisation. Full dispatch logic is
// implemented in Steps 2–4; this stub returns a method-not-found error for any
// method other than "initialize".
func (s *Server) handle(ctx smeldr.Context, req jsonRPCRequest) jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  s.handleInitialize(),
		}
	default:
		if r, ok := s.handleToolMethod(ctx, req); ok {
			return r
		}
		if r, ok := s.handleResourceMethod(ctx, req); ok {
			return r
		}
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &jsonRPCError{
				Code:    -32601,
				Message: "method not found: " + req.Method,
			},
		}
	}
}

// handleInitialize returns the MCP initialize response payload.
func (s *Server) handleInitialize() any {
	return map[string]any{
		"protocolVersion": "2024-11-05",
		"serverInfo":      map[string]any{"name": "forge-mcp", "version": "1.0.0"},
		"capabilities": map[string]any{
			"resources": map[string]any{"subscribe": true, "listChanged": true},
			"tools":     map[string]any{"listChanged": false},
		},
	}
}

// jsonRPCRequest is the wire format for an incoming JSON-RPC 2.0 request.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonRPCResponse is the wire format for an outgoing JSON-RPC 2.0 response.
type jsonRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id"`
	Result  any           `json:"result,omitempty"`
	Error   *jsonRPCError `json:"error,omitempty"`
}

// jsonRPCError is the error object within a JSON-RPC 2.0 response.
type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// hasMCPOp reports whether m's Operations slice contains op.
func hasMCPOp(m smeldr.MCPModule, op smeldr.MCPOperation) bool {
	for _, o := range m.MCPMeta().Operations {
		if o == op {
			return true
		}
	}
	return false
}

// slugOf extracts the Slug field from an item via the smeldr.Node GetSlug method.
// Returns "" when the item does not implement the interface.
func slugOf(item any) string {
	type slugger interface{ GetSlug() string }
	if s, ok := item.(slugger); ok {
		return s.GetSlug()
	}
	return ""
}

// snakeCase converts a PascalCase or camelCase string to lower_snake_case.
// Consecutive uppercase letters are treated as a single word:
//
//	BlogPost → blog_post
//	MCPPost  → mcp_post
//
// NOTE: This function is intentionally duplicated in module.go (forge core).
// The two packages cannot import each other, so each carries its own copy.
// Any change to the algorithm here must be mirrored in module.go, and vice versa.
func snakeCase(s string) string {
	runes := []rune(s)
	var out []rune
	for i, r := range runes {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				prev := runes[i-1]
				prevLow := (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9')
				prevUp := prev >= 'A' && prev <= 'Z'
				if prevLow {
					out = append(out, '_')
				} else if prevUp {
					if i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z' {
						out = append(out, '_')
					}
				}
			}
			out = append(out, r+32)
		} else {
			out = append(out, r)
		}
	}
	return string(out)
}
