# smeldr.dev/mcp

> ✅ **Available** — MCP (Model Context Protocol) server for Smeldr apps.

`smeldr.dev/mcp` wraps a `smeldr.App` and exposes its `MCP`-registered content modules as
MCP resources and tools, enabling Claude Desktop, Cursor, and any MCP-compatible AI
assistant to create, read, update, publish, and delete content through the Model
Context Protocol.

Schema derivation, role enforcement, and lifecycle rules are automatic — they use the
same code paths as the HTTP layer. There is no special MCP bypass.

---

## Quick start

```bash
go get smeldr.dev/mcp
```

```go
package main

import (
	"context"
	"os"

	forgemcp "smeldr.dev/mcp"

	"smeldr.dev/core"
)

// BlogPost is your content type — embed smeldr.Node and add your fields.
type BlogPost struct {
	smeldr.Node
	Title string `smeldr:"required" json:"title"`
	Body  string `smeldr:"required,min=50" json:"body"`
}

func main() {
	app := smeldr.New(smeldr.Config{
		BaseURL: "https://mysite.com",
		Secret:  []byte(os.Getenv("SECRET")),
	})

	posts := smeldr.NewModule((*BlogPost)(nil),
		smeldr.At("/posts"),
		smeldr.Repo(smeldr.NewMemoryRepo[*BlogPost]()),
		smeldr.MCP(smeldr.MCPWrite), // expose as MCP tools
	)
	app.Content(posts)

	srv := forgemcp.New(app)
	if err := srv.ServeStdio(context.Background(), os.Stdin, os.Stdout); err != nil {
		os.Exit(1)
	}
}
```

Build the binary:

```bash
go build -o myapp-mcp .
```

---

## Claude Desktop

Add to `~/Library/Application Support/Claude/claude_desktop_config.json`
(macOS) or `%APPDATA%\Claude\claude_desktop_config.json` (Windows):

```json
{
  "mcpServers": {
    "myapp": {
      "command": "/absolute/path/to/myapp-mcp",
      "env": {
        "SECRET": "your-hmac-secret"
      }
    }
  }
}
```

Replace `/absolute/path/to/myapp-mcp` with the path to the compiled binary.
Restart Claude Desktop to pick up the new server.

---

## Cursor

Add to `.cursor/mcp.json` in your project root (or `~/.cursor/mcp.json` globally):

```json
{
  "mcpServers": {
    "myapp": {
      "command": "/absolute/path/to/myapp-mcp",
      "env": {
        "SECRET": "your-hmac-secret"
      }
    }
  }
}
```

---

## SSE (remote / authenticated)

For remote access over HTTP, use the SSE transport:

```go
// In your main app or a dedicated MCP server binary:
mux := http.NewServeMux()
mux.Handle("/", forgemcp.New(app).Handler())
http.ListenAndServe(":9090", mux)
```

The SSE handler registers two routes:

- `GET /mcp` — establishes the event stream; the client keeps this connection open.
- `POST /mcp/message` — accepts JSON-RPC requests. Requires `Authorization: Bearer <token>`.

### Getting a token

Use `smeldr.SignToken` to mint a token with the same HMAC secret as your app:

```go
token, err := smeldr.SignToken(smeldr.User{
	ID:    "alice",
	Roles: []smeldr.Role{smeldr.Author},
}, []byte(os.Getenv("SECRET")), 0) // 0 = no expiry
```

### MCP client configuration (SSE)

```json
{
  "mcpServers": {
    "myapp-remote": {
      "url": "https://mysite.com/mcp",
      "headers": {
        "Authorization": "Bearer eyJ..."
      }
    }
  }
}
```

---

## MCPRead vs MCPWrite

Register each module with the operations you want:

```go
smeldr.MCP(smeldr.MCPRead)                    // read-only access
smeldr.MCP(smeldr.MCPWrite)                   // write-only access
smeldr.MCP(smeldr.MCPRead, smeldr.MCPWrite)    // full access
```

| MCP method                  | Requires   | Role required | What it does                                                  |
|-----------------------------|------------|---------------|---------------------------------------------------------------|
| `resources/list`            | MCPRead    | any           | List all Published items across all MCPRead modules           |
| `resources/templates/list`  | MCPRead    | any           | Return the URI template for each MCPRead module               |
| `resources/read {uri}`      | MCPRead    | any           | Fetch a single Published item by URI                          |
| `tools/call create_{type}`  | MCPWrite   | Author+       | Create a Draft item; returns the saved item as JSON           |
| `tools/call update_{type}`  | MCPWrite   | Author+       | Partially update fields by slug; non-supplied fields retained |
| `tools/call publish_{type}` | MCPWrite   | Author+       | Transition Draft → Published; idempotent                      |
| `tools/call schedule_{type}`| MCPWrite   | Author+       | Set Scheduled status and `scheduled_at` (RFC 3339)            |
| `tools/call archive_{type}` | MCPWrite   | Author+       | Set Archived status                                           |
| `tools/call delete_{type}`  | MCPWrite   | Author+       | Permanently delete an item                                    |
| `tools/call list_{type}s`   | MCPWrite   | Editor+       | List all items at any status; optional `status` filter        |
| `tools/call get_{type}`     | MCPWrite   | Editor+       | Get a single item by slug at any status                       |

**Tool naming:** type names are converted to `lower_snake_case`, with consecutive
uppercase letters treated as one word. `BlogPost` → `blog_post`,
`MCPDocument` → `mcp_document`. Full examples:
`create_blog_post`, `update_blog_post`, `publish_blog_post`.

**Resource URI format:** `forge://{prefix}/{slug}` — for example,
`forge://posts/hello-world` for a post at `/posts/hello-world`.

**Field names in `create_*` and `update_*` arguments:** use the JSON field name
(lowercase, respecting any `json:` tag). For `Title string` with no json tag,
the argument key is `"title"`. For `Tags string \`json:"tags"\``, the key is `"tags"`.

---

## Admin read tools

Each MCPWrite module also exposes two admin read tools that require **Editor or Admin** role.
Unlike `resources/read`, these tools return content at any lifecycle status — Draft, Scheduled,
Published, or Archived.

| Tool | Arguments | Returns |
|------|-----------|--------|
| `list_{type}s` | `status?: "draft"\|"scheduled"\|"published"\|"archived"` | `[]T` — all items, or filtered by status |
| `get_{type}` | `slug: string` | `T` — single item |

```go
// Example: list all posts regardless of status
// Tool name: list_blog_posts (type BlogPost → snake blog_post + s)

// Example: get a single post by slug
// Tool name: get_blog_post
```

**Tool naming:** follows the same snake_case rule as write tools. `BlogPost` → `blog_post`,
so the list tool is `list_blog_posts` and the get tool is `get_blog_post`.

**Return value:** the full struct including all `smeldr.Node` fields (`Status`, `PublishedAt`,
`ScheduledAt`, `CreatedAt`, `UpdatedAt`, `Slug`, `ID`).

---

## Block system tools (`WithBlocks`)

Enable the block-system tools with the `WithBlocks` option. They let an AI operator
create generic blocks and compose them into pages and collections. The tools read
and write the `smeldr_dynamic_content` and `smeldr_content_edges` tables — create
them once at startup with `smeldr.CreateBlockTables(db)`.

```go
smeldr.CreateBlockTables(db)
mcpSrv := forgemcp.New(app, forgemcp.WithBlocks())
```

Blocks are addressed by **ID** (they have no slug) and are not exposed as
`resources/*` — read them with `get_node` / `list_nodes`.

| Tool | Role | Arguments |
|------|------|-----------|
| `create_node` | Author+ | `type_name`, `fields` (object) → `{id, type_name, status, slug}` |
| `update_node` | Author+ | `id`, `fields` (merged onto stored; `type_name` immutable) |
| `get_node` | Author+ | `id` |
| `list_nodes` | Author+ | `type_name?`, `status?` |
| `publish_node` / `archive_node` | Author+ | `id` (publish idempotent) |
| `add_section` / `add_item` | Editor+ | `parent_id`, `child_id` (types derived) |
| `reorder_sections` / `reorder_items` | Editor+ | `parent_id`, `ordered_child_ids` |
| `remove_section` / `remove_item` | Editor+ | `parent_id`, `child_id` |

Sections (`edge_role` `"section"`) compose pages; items (`"item"`) compose
collections. The names are distinct for clarity; both share one implementation.

---

## Lifecycle enforcement

**MCPRead exposes only `Published` items.** Draft, Scheduled, and Archived items
are invisible to `resources/list` and return MCP error -32001 on `resources/read`.
This enforcement is unconditional — it applies regardless of the caller's role and
cannot be disabled.

MCPWrite tools operate on items at any lifecycle stage. The full transition table:

```
Draft      → Published  (publish_{type})
Draft      → Scheduled  (schedule_{type}, requires scheduled_at RFC 3339)
*, *       → Archived   (archive_{type})
Published  → Published  (publish_{type} is idempotent — no error, no duplicate signal)
```

The Smeldr scheduler automatically transitions `Scheduled → Published` at the
configured time without any MCP call.

---

## Role enforcement

### stdio transport (ServeStdio)

`ServeStdio` runs with `smeldr.Admin` privileges. It is designed for **local,
trusted processes only** — any process that can write to stdin has full content
access. Do not expose the MCP binary's stdin to untrusted processes.

### SSE transport (Handler)

`POST /mcp/message` enforces authentication on every request:

- No `Authorization: Bearer <token>` header → HTTP 401
- Valid token, but role < `smeldr.Author` → MCP error -32001 (not HTTP 401;
  authentication succeeded, authorisation failed)
- All `tools/call` operations require `smeldr.Author` role or above

Tokens are minted and verified using the same HMAC mechanism as the Forge REST
API (`smeldr.BearerHMAC`). A token from `smeldr.SignToken(user, secret, 0)` is
valid for both the REST API and the SSE MCP transport — there is no separate
token format.

---

## Zero dependencies

`smeldr.dev/mcp` has zero external dependencies. All MCP transport and JSON-RPC 2.0
protocol handling is implemented using the Go standard library only.

---

## Secret rotation (WithSecret)

`forgemcp.New(app)` automatically inherits the HMAC secret from `app.Config.Secret`.
You only need `WithSecret` during a secret rotation window — when you need to
accept tokens signed with a different secret than the one currently in `Config.Secret`:

```go
srv := forgemcp.New(app, forgemcp.WithSecret(oldSecret))
```

If the provided secret differs from `app.Config.Secret`, a warning is logged at
startup. Remove `WithSecret` once rotation is complete.
