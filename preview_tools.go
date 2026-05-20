package forgemcp

import (
	"fmt"
	"strings"

	"forge-cms.dev/forge"
)

// previewToolDefs returns the single Admin-only preview URL tool definition.
func previewToolDefs() []mcpTool {
	return []mcpTool{
		{
			Name:        "create_preview_url",
			Description: "Generate a signed preview URL for a draft content item. The URL bypasses the Published-only visibility guard for the token's lifetime (default 12 h). Requires Admin role.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prefix": map[string]any{
						"type":        "string",
						"description": `URL prefix of the content module, e.g. "/posts" or "/docs". Must include the leading slash.`,
					},
					"slug": map[string]any{
						"type":        "string",
						"description": "Slug of the content item to preview, e.g. \"my-draft-post\".",
					},
				},
				"required": []string{"prefix", "slug"},
			},
		},
	}
}

// isPreviewTool reports whether name is the preview URL tool.
func isPreviewTool(name string) bool { return name == "create_preview_url" }

// handlePreviewTool executes the create_preview_url tool.
// Requires Admin role (verified by the caller in tool.go before dispatch).
// app is the forge.App passed to [New]; it carries BaseURL and GeneratePreviewToken.
func (s *Server) handlePreviewTool(app *forge.App, name string, args map[string]any) (any, *jsonRPCError) {
	if name != "create_preview_url" {
		return nil, &jsonRPCError{Code: -32601, Message: "unknown preview tool: " + name}
	}

	prefix, _ := args["prefix"].(string)
	slug, _ := args["slug"].(string)

	prefix = strings.TrimSpace(prefix)
	slug = strings.TrimSpace(slug)

	if prefix == "" {
		return nil, &jsonRPCError{Code: -32602, Message: "prefix is required"}
	}
	if slug == "" {
		return nil, &jsonRPCError{Code: -32602, Message: "slug is required"}
	}
	if !strings.HasPrefix(prefix, "/") {
		return nil, &jsonRPCError{Code: -32602, Message: "prefix must begin with /"}
	}

	token := app.GeneratePreviewToken(prefix, slug)

	// For SingleInstance modules, the slug route does not exist — the item is
	// served directly at /{prefix}. Build the URL without the slug segment.
	isSingleInstance := false
	for _, m := range s.modules {
		if m.MCPMeta().Prefix == prefix {
			isSingleInstance = m.MCPMeta().SingleInstance
			break
		}
	}

	var previewURL string
	if isSingleInstance {
		previewURL = fmt.Sprintf("%s%s?preview=%s", app.BaseURL(), prefix, token)
	} else {
		previewURL = fmt.Sprintf("%s%s/%s?preview=%s", app.BaseURL(), prefix, slug, token)
	}

	return toolResult(previewURL), nil
}
