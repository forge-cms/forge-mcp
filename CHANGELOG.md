# Changelog — forge-mcp

All notable changes to the `forge-mcp` module are documented here.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning: [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [1.11.0] — 2026-05-24

OAuth 2.1 integration via `WithOAuth` — remote MCP servers for ChatGPT Plus and Claude.ai.

### Added

- `forgemcp.WithOAuth(oauth *forgeoauth.Server) ServerOption` — enables OAuth 2.1
  authentication. When set, all HTTP requests (GET /mcp SSE and POST /mcp/message)
  require a valid Bearer access token issued by the forge-oauth authorization server.
  Unauthenticated requests return HTTP 401 with `WWW-Authenticate: Bearer resource_metadata=...`.

- `GET /.well-known/oauth-protected-resource` — RFC 9728 Protected Resource Metadata.
  Returns JSON identifying this MCP server and its authorization server. Returns 404
  when OAuth is not enabled.

- `GET /oauth/*` and `POST /oauth/*` — OAuth 2.1 endpoints (authorization, token) are
  mounted as a catch-all when `WithOAuth` is configured. Served by `forge-cms.dev/forge-oauth`.

- `GET /.well-known/oauth-authorization-server` — RFC 8414 metadata served by forge-oauth.

- `oauthScopeToRole` scope-to-role mapping:
  - `mcp` → `forge.Author` (standard AI assistant scope)
  - `mcp:admin` → `forge.Admin`
  - All other values → `forge.Author` (safe default)

### Changed

- `Server.Handler()` now always registers `GET /.well-known/oauth-protected-resource`
  (returns 404 when OAuth is not configured, 200 JSON when enabled).

### Dependencies

- `forge-cms.dev/forge` bumped to v1.25.0 (adds `forge.VerifyTokenString`).
- `forge-cms.dev/forge-oauth v0.1.0` added (new dependency).

---

## [1.10.3] — 2026-05-21

### Fixed

- `tool.go`: lower `create_preview_url` minimum role from Admin to Editor.
  Brand and site pilots hold Editor tokens and could not generate preview URLs.
- `preview_tools.go`: update tool description to reflect "Requires Editor or Admin role."

---

## [1.10.2] — 2026-05-20

### Fixed

- `preview_tools.go`: `create_preview_url` now returns `{baseURL}/{prefix}?preview={token}`
  for SingleInstance modules. Previously it always appended `/{slug}` to the path, which
  returned 404 for SingleInstance modules where the slug route is not registered.
  Normal modules are unaffected.

---

## [1.10.1] — 2026-05-20

### Fixed

- `go.mod`: bumped `forge-cms.dev/forge` require from v1.19.0 to v1.23.0.
  v1.10.0 uses `MCPMeta.SingleInstance` (added in forge v1.23.0) but declared
  the wrong minimum dependency — causing a module mismatch for consumers.

---

## [1.10.0] — 2026-05-23

SingleInstance support: suppress `list_{type}s` admin tool (Amendment A101).

### Changed

- `mcpAdminReadToolDefs`: when `MCPMeta.SingleInstance` is `true`, the
  `list_{type}s` tool is omitted from the generated admin tool set. A
  single-instance module has at most one item — `get_{type}` is sufficient
  for content management. The `get_{type}` and `delete_{type}` tools are
  always generated.

### Requires

- `forge-cms.dev/forge` ≥ v1.23.0 (for `MCPMeta.SingleInstance`).

---

## [1.9.3] — 2026-05-17

### Fixed

- `sseHandler` no longer sends a non-spec `event: open` keepalive before
  `event: endpoint`. The go-sdk v1.6.0 `SSEClientTransport` expects `endpoint`
  as the first SSE event per the MCP SSE spec; the `open` event caused
  forge-agent connections to fail with "missing endpoint: first event is open".

---

## [1.9.2] — 2026-05-09

Patch release — no code changes. Re-tag to refresh module proxy cache after
v1.9.1 was cached with an incorrect module declaration in `go.mod`.

---

## [1.9.1] — 2026-05-09

Patch release — no code changes. Bumps forge dependency from v1.18.0 to v1.19.0
in go.mod so the module proxy serves the correct dependency graph.

---

## [1.9.0] — 2026-05-09

Upload token MCP tool (Milestone 13, Amendment A93).

### Added

- `create_upload_token` MCP tool (Author+ role) — takes no parameters; returns
  `{ token, upload_url, expires_in }`. The token is passed to `POST /media` as
  `Authorization: UploadToken <token>`. UploadToken uploads are restricted to image
  MIME types by forge-media.
- `upload_tools.go`: tool definition, dispatch, and `handleUploadTool`.
- `tool.go`: `uploadToolDefs()` always appended in `handleToolsList` (not gated on
  a store); `isUploadTool` dispatch block in `handleToolsCall` (Author gate).

---

## [1.8.1] — 2026-05-08

Patch release — no code changes. Re-tag to refresh module proxy cache after
`go.mod` was updated to require `forge-cms.dev/forge v1.18.0` (was v1.14.1).

---

## [1.8.0] — 2026-05-08

Draft preview URL tool (Milestone 12, Amendment A92).

### Added

- `create_preview_url` MCP tool (Admin role) — takes `prefix` (e.g. `/posts`)
  and `slug`; returns the full signed preview URL. The URL grants read access
  to Draft or Scheduled content for the token lifetime (default 12 h). Archived
  items are never previewable regardless of token validity.
- `preview_tools.go`: tool definition, validation, and dispatch.
- `Server.app` field: stores the `*forge.App` reference for `BaseURL()` and
  `GeneratePreviewToken()` calls; set in `New`.

### Changed

- `tools/list` always includes `create_preview_url` (not gated on a store).

---

## [1.7.0] — 2026-05-08

Outbound webhook MCP tools and MCP resource subscriptions (Milestone 11).

### Added

- `webhook_tools.go`: 5 new Admin-role MCP tools — `create_webhook`,
  `list_webhooks`, `delete_webhook`, `list_webhook_deliveries`, `retry_webhook`.
  All require Admin role. `create_webhook` validates HTTPS URLs server-side
  (SSRF protection). Signing secrets are returned once at creation.
- `subscription.go`: `subscriptionRegistry` — session-keyed fan-out registry
  for SSE push notifications. `buildNotifyEvent`, `newSessionID`.
- `mcp.go`: `subscriptions *subscriptionRegistry` field; `New()` wires a
  signal listener that calls `subscriptions.Notify(uri)` on content changes;
  `handleInitialize` now returns `"resources": {"subscribe": true, "listChanged": true}`.
- `transport.go`: SSE transport assigns a per-connection session ID; sends
  `event: endpoint` with the session-scoped message URL; notification loop
  forwards `notifications/resources/updated` events to subscribed clients.
- `resource.go`: `handleResourceMethod` routes `resources/subscribe` and
  `resources/unsubscribe` JSON-RPC methods.
- `subscription_test.go`: 6 unit tests for the subscription registry.
- `tool.go`: webhook tool dispatch wired into `handleToolsList` and `handleToolsCall`.

---

## [1.6.1] — 2026-05-02

Patch release — no code changes. Re-tag to refresh module proxy cache after
vanity URL migration to `forge-cms.dev`.

---

## [1.6.0] — 2026-04-30

Go 1.26.2 and module path migration to `forge-cms.dev` (Amendment A76).

### Changed

- `go.mod`: module path renamed from `github.com/forge-cms/forge-mcp` to
  `forge-cms.dev/forge-mcp`; `go` directive bumped from `1.22` to `1.26.2`.
- All imports of `github.com/forge-cms/forge` updated to `forge-cms.dev/forge`.

---

## [1.5.0] — 2026-04-18

forge-media integration — `WithModule` server option (Decision 31).

### Added

- `mcp.go`: `WithModule(m forge.MCPModule) ServerOption` — registers an additional
  `forge.MCPModule` with the MCP server. Enables external sub-packages such as
  `forge-media` to expose their content through the same MCP server without
  registering through `forge.App.MCPModules()`.

---

## [1.4.0] — 2026-04-11

NavTree MCP tools — four new nav tools (Decision 29).

### Added

- `mcp.go`: `Server.navTree *forge.NavTree` field; wired from `app.NavTree()` in `New()`.
- `tool.go`: `navToolDefs(hasDB bool)` — returns `list_nav_items` (always available);
  adds `create_nav_item`, `update_nav_item`, `delete_nav_item` when tree is DB-backed.
  All require Editor or Admin role.
- `tool.go`: `handleNavTool()` — dispatches four nav tools. `update_nav_item` uses
  partial-overlay semantics: absent fields are preserved from the stored item.
- `tool.go`: `stringArgOr`, `boolArgOr`, `intArgOr` helper functions for optional arg extraction.
- `tool.go`: `handleToolsList()` — appends nav tools when `s.navTree != nil`.
- `tool.go`: `handleToolsCall()` — pre-dispatches nav tools (Editor gate) before generic module routing.

---

## [1.3.1] — 2026-04-10

Fix invalid `"type":"datetime"` in generated JSON Schema for time fields.

### Fixed

- `mcp.go`: `inputSchema` and `inputSchemaUpdate` now emit
  `{"type":"string","format":"date-time"}` for fields with internal type
  `"datetime"` (`time.Time` fields such as `published_at` and `scheduled_at`).
  Previously emitted the invalid `"type":"datetime"`, which caused VS Code
  Copilot agent mode and other strict MCP clients to reject tool registration.

---

## [1.3.0] — 2026-04-07

Field format semantics: emit `"description"` in tool input schemas (Decision 27).

### Added

- `mcp.go`: `fieldDescription` helper implementing the three-case priority rule
  from Decision 27 — both tags, format-only, neither (Decision 27).
- `mcp.go`: `inputSchema` and `inputSchemaUpdate` now set `"description"` in each
  JSON Schema property when `forge.MCPField.Format` or `.Description` is non-empty
  (Decision 27).

---

## [1.2.0] — 2026-04-06

Surface last-admin guard error as actionable MCP message (Decision 26).

### Changed

- `tool.go`: `handleTokenTool` — `revoke_token` now detects `forge.ErrLastAdmin`
  and returns a specific, actionable JSON-RPC error message directing the operator
  to create a replacement admin token before revoking (Decision 26).

---

## [1.1.0] — 2026-04-05

Named revocable bearer token tools — Admin role required (Amendment A66).

### Added

- `mcp.go`: `Server.tokenStore *forge.TokenStore` field; wired from
  `app.TokenStore()` in `New()`.
- `transport.go`: `VerifyBearerToken` call updated to pass `s.tokenStore`
  (3-arg signature).
- `tool.go`: `authoriseAdmin()` helper enforcing Admin role (JSON-RPC -32001
  on failure); `tokenToolDefs()` returning three MCP tool definitions
  (`create_token`, `list_tokens`, `revoke_token`) with JSON Schema; 
  `handleTokenTool()` dispatcher; `handleToolsList()` appends token tools
  when `s.tokenStore != nil`; `handleToolsCall()` pre-dispatches token
  tool names before module-level auth.

### Token tools (Admin role required)

| Tool | Description |
|------|-------------|
| `create_token` | Issues a named bearer token with a given role and TTL in days |
| `list_tokens` | Lists all tokens with name, role, expiry, and revocation status |
| `revoke_token` | Revokes a token by ID — effective on the next request |

---

## [1.0.5] — 2026-03-18

`delete_{type}` moved to Editor-level admin tools (Amendment A55).

### Changed

- `mcp.go`: `delete_{type}` moved from `mcpToolDefs` (Author-required write
  tools) to `mcpAdminReadToolDefs` (Editor-required admin tools); the total
  tool count per MCPWrite module remains 8 (5 write + 3 admin: list, get,
  delete); `mcpAdminReadToolDefs` now accepts a local `slugOnly` schema to
  avoid repeating the object definition
- `tool.go`: `delete` dispatch case now calls `authoriseEditor` before
  executing the delete; previously only Author role was required — now
  Editor or Admin is required

---

## [1.0.4] — 2026-03-18

Wrap all tool call results in MCP `CallToolResult` format.

### Fixed

- `tool.go`: `handleToolsCall` now wraps every successful result in the MCP
  `CallToolResult` envelope via a new `toolResult` helper:
  `{"content":[{"type":"text","text":"<json>"}],"isError":false}`;
  previously returned raw Go values that serialised to plain JSON objects
  or arrays, causing MCP clients (including Claude Desktop) to silently
  discard the result or display empty output

---

## [1.0.3] — 2026-03-18

Fix `list_{type}s` response format: wrap slice in `{"items": [...]}` object.

### Fixed

- `tool.go`: `list` case in `handleToolsCall` now returns
  `map[string]any{"items": items}` instead of a raw `[]any`; a bare JSON
  array result caused MCP protocol validation errors in clients that
  interpret array-valued tool results as batch responses

---

## [1.0.2] — 2026-03-18

Admin read tools for MCPWrite modules.

### Added

- `mcp.go`: `mcpAdminReadToolDefs` generates two tools per MCPWrite module:
  `list_{type}s` (all items, optional `status` filter) and `get_{type}`
  (single item by slug); both tools return items at any lifecycle status
- `tool.go`: `authoriseEditor` role check (Editor or Admin); `moduleForAdminList`
  resolves the plural typeSnake used by `list_{type}s` tool names; `list` and
  `get` cases in `handleToolsCall`; `handleToolsList` updated to include admin
  read tools alongside write tools

---

## [1.0.1] — 2026-03-17

`inputSchema` and `inputSchemaUpdate` now emit the correct JSON Schema for
`[]string` fields (Amendment A52-2).

### Fixed

- `mcp.go`: `inputSchema` and `inputSchemaUpdate` now emit
  `{"type":"array","items":{"type":"string"}}` for fields with `Type == "array"`;
  previously emitted bare `{"type":"array"}` without an `items` declaration, and
  incorrectly applied `minLength`/`maxLength`/`enum` constraints to array fields
  (Amendment A52-2)

---

## [1.0.0] — 2026-03-17

Initial release of `forge-mcp` — MCP support for Forge apps (Milestone 10).

### Added

- `mcp.go`: `Server` struct; `New(app, opts...)` constructor; `ServerOption`
  interface; `WithSecret(secret []byte)` option; `handle` JSON-RPC dispatcher;
  `handleInitialize`; JSON-RPC wire types (`jsonRPCRequest`, `jsonRPCResponse`,
  `jsonRPCError`); `mcpTool`, `mcpResource`, `allResources`, `mcpToolDefs`,
  `inputSchema`, `inputSchemaUpdate` helpers; `hasMCPOp`, `slugOf`, `snakeCase`
  utilities
- `resource.go`: `handleResourceMethod`, `handleResourcesList`,
  `handleResourcesTemplatesList`, `handleResourcesRead`, `parseResourceURI`;
  `mcpResource`, `resourceContent`, `resourceTemplate` wire types;
  Published-only lifecycle enforcement for MCP resources/read
- `tool.go`: `handleToolMethod`, `handleToolsList`, `handleToolsCall`
  dispatcher (create/update/publish/schedule/archive/delete); `toolName`,
  `parseToolName`, `moduleForType`, `authorise`, `errorFor`, `stringArg`
  helpers; Author-level role enforcement; idempotent publish; delete response
  `{"deleted":true,"slug":...}`
- `transport.go`: `ServeStdio(ctx context.Context, in io.Reader, out io.Writer)`
  for local stdio transport (Claude Desktop, Cursor, CLI tools); `Handler()`
  returning an `http.Handler` with SSE keepalive (`GET /mcp`) and authenticated
  JSON-RPC endpoint (`POST /mcp/message`) for remote SSE transport; 1 MiB
  request body limit; HMAC Bearer token authentication via `forge.VerifyBearerToken`
