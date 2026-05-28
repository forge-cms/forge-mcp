package forgemcp

import (
	"time"

	"smeldr.dev/core"
)

// uploadToolDefs returns the single Author+ upload token tool definition.
func uploadToolDefs() []mcpTool {
	return []mcpTool{
		{
			Name:        "create_upload_token",
			Description: "Generate a short-lived upload token for POST /media. Pass the token in the Authorization header as \"UploadToken <token>\". UploadToken uploads are restricted to image MIME types (jpeg, png, webp, gif, avif). Requires Author role.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
				"required":   []string{},
			},
		},
	}
}

// isUploadTool reports whether name is the upload token tool.
func isUploadTool(name string) bool { return name == "create_upload_token" }

// handleUploadTool executes the create_upload_token tool.
// Requires Author role (verified by the caller in tool.go before dispatch).
// app is the smeldr.App passed to [New]; it carries GenerateUploadToken and BaseURL.
func (s *Server) handleUploadTool(app *smeldr.App, name string) (any, *jsonRPCError) {
	if name != "create_upload_token" {
		return nil, &jsonRPCError{Code: -32601, Message: "unknown upload tool: " + name}
	}

	token := app.GenerateUploadToken()

	ttl := app.Config().MediaUploadTokenExpiry
	if ttl == 0 {
		ttl = 15 * time.Minute
	}

	return toolResult(map[string]any{
		"token":      token,
		"upload_url": app.BaseURL() + "/media",
		"expires_in": int(ttl.Seconds()),
	}), nil
}
