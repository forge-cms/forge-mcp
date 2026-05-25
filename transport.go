package forgemcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"forge-cms.dev/forge"
	forgeoauth "forge-cms.dev/forge-oauth"
)

// ServeStdio runs the MCP server over newline-delimited JSON on the given
// reader and writer. It is intended for use with local AI tools such as Claude
// Desktop and Cursor that launch the server as a child process and communicate
// over stdin/stdout:
//
//	srv.ServeStdio(ctx, os.Stdin, os.Stdout)
//
// Each request is read as a single JSON line, dispatched through [Server.handle],
// and the response is written as a single JSON line followed by a newline.
// Empty lines are skipped. Malformed JSON returns a -32700 parse-error response.
// ServeStdio returns when ctx is cancelled or in reaches EOF.
//
// The stdio transport runs with [forge.Admin] privileges — the process runs
// locally and the operator is trusted.
func (s *Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	forgeCtx := forge.NewContextWithUser(forge.User{
		ID:    "stdio",
		Roles: []forge.Role{forge.Admin},
	})

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)

	lineCh := make(chan string)
	errCh := make(chan error, 1)
	go func() {
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		errCh <- scanner.Err()
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errCh:
			return err
		case line := <-lineCh:
			if strings.TrimSpace(line) == "" {
				continue
			}
			var resp jsonRPCResponse
			var req jsonRPCRequest
			if err := json.Unmarshal([]byte(line), &req); err != nil {
				resp = jsonRPCResponse{
					JSONRPC: "2.0",
					Error:   &jsonRPCError{Code: -32700, Message: "parse error"},
				}
			} else {
				resp = s.handle(forgeCtx, req)
			}
			b, _ := json.Marshal(resp)
			fmt.Fprintf(out, "%s\n", b)
		}
	}
}

// Handler returns an [http.Handler] that serves the MCP protocol over HTTP.
// Routes registered on a fresh [http.ServeMux]:
//
//   - GET  /mcp                                    — SSE endpoint (MCP 2024-11-05 transport)
//   - POST /mcp                                    — JSON-RPC endpoint (MCP 2025-11-25 streamable HTTP)
//   - POST /mcp/message                            — JSON-RPC endpoint (MCP 2024-11-05 SSE transport)
//   - GET  /.well-known/oauth-protected-resource   — RFC 9728 metadata (404 when OAuth not enabled)
//
// When [WithOAuth] is configured, additional OAuth 2.1 endpoints are mounted:
//
//   - GET  /.well-known/oauth-authorization-server — RFC 8414 metadata
//   - GET  /oauth/authorize                        — authorization form
//   - POST /oauth/authorize                        — form submission
//   - POST /oauth/token                            — code exchange and token refresh
//
// Authentication when OAuth is enabled ([WithOAuth]):
//   - All HTTP requests (GET /mcp and POST /mcp/message) must carry a valid
//     OAuth Bearer access token. Missing or invalid tokens return HTTP 401 with
//     a WWW-Authenticate header pointing to the protected resource metadata.
//   - When [WithForgeFallback] is also set, forge bearer tokens are accepted as
//     a fallback if the token is not found in the OAuth store. Expired OAuth
//     tokens are never eligible for fallback.
//
// Authentication without OAuth (forge bearer tokens):
//   - If the server was constructed with a non-empty secret (auto-inherited from
//     [forge.App] via [New], or set via [WithSecret]), POST /mcp/message requires
//     a valid "Authorization: Bearer <token>" header. GET /mcp is unauthenticated.
//   - If no secret is configured, requests are treated as [forge.GuestUser].
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /mcp", s.sseHandler)
	mux.HandleFunc("POST /mcp", s.messageHandler)
	mux.HandleFunc("POST /mcp/message", s.messageHandler)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.protectedResourceHandler)
	if s.oauth != nil {
		// Mount the OAuth server as a catch-all. Go 1.22+ ServeMux gives priority
		// to more-specific patterns registered above, so /mcp routes are unaffected.
		mux.Handle("/", s.oauth.Handler())
	}
	return mux
}

// sseHandler handles GET /mcp, upgrading the HTTP connection to a Server-Sent
// Events stream. It:
//  1. Sends an initial "event: open" keepalive for backward compatibility.
//  2. Generates a unique session ID and registers a send function with the
//     subscription registry so that resource-update notifications can be
//     pushed over this stream.
//  3. Sends "event: endpoint\ndata: /mcp/message?session_id=<id>\n\n" so the
//     client knows which URL to include when calling POST /mcp/message.
//  4. Blocks in a select loop, writing resource notifications until the client
//     disconnects or the request context is cancelled.
//  5. Calls RemoveConn on disconnect to release the registry entry.
func (s *Server) sseHandler(w http.ResponseWriter, r *http.Request) {
	// When OAuth is enabled, the SSE endpoint requires a valid Bearer token.
	// A 401 response here triggers the OAuth flow in AI clients (ChatGPT, Claude.ai).
	// When WithForgeFallback is also set, a forge bearer token is accepted as a
	// fallback if the OAuth store does not recognise it (ErrTokenNotFound).
	if s.oauth != nil {
		token := extractBearerToken(r)
		if token == "" {
			s.writeOAuthChallenge(w)
			return
		}
		_, err := s.oauth.ValidateAccessToken(r.Context(), token)
		if err != nil {
			if s.forgeFallback && errors.Is(err, forgeoauth.ErrTokenNotFound) {
				if _, ok := forge.VerifyTokenString(token, s.secret, s.tokenStore); !ok {
					s.writeOAuthChallenge(w)
					return
				}
				// Valid forge bearer token — fall through to SSE stream.
			} else {
				s.writeOAuthChallenge(w)
				return
			}
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Allocate a session and register the send function before announcing the
	// endpoint URL. This ensures no notifications are dropped between the
	// endpoint announcement and the client's first subscribe call.
	sessionID := newSessionID()
	notifCh := make(chan string, 32)
	if s.subscriptions != nil {
		s.subscriptions.RegisterSend(sessionID, func(uri string) {
			select {
			case notifCh <- buildNotifyEvent(uri):
			default: // drop if the channel is full; slow consumers miss stale notifications
			}
		})
		defer s.subscriptions.RemoveConn(sessionID)
	}

	// Announce the session-scoped message endpoint to the client.
	fmt.Fprintf(w, "event: endpoint\ndata: /mcp/message?session_id=%s\n\n", sessionID)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-notifCh:
			fmt.Fprint(w, event)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
}

// messageHandler handles POST /mcp/message. It enforces the authentication
// boundary (HTTP 401) before any JSON-RPC decoding, applies a 1 MiB body
// limit, and writes a JSON-RPC response for every outcome.
func (s *Server) messageHandler(w http.ResponseWriter, r *http.Request) {
	// Authentication boundary: HTTP-level 401 before any JSON-RPC decoding.
	var forgeCtx forge.Context
	if s.oauth != nil {
		// OAuth 2.1 mode: validate Bearer access token issued by forge-oauth.
		// When WithForgeFallback is set, a forge bearer token is accepted as a
		// fallback if the token is not found in the OAuth store (ErrTokenNotFound).
		// An expired OAuth token is never eligible for fallback.
		token := extractBearerToken(r)
		if token == "" {
			s.writeOAuthChallenge(w)
			return
		}
		at, err := s.oauth.ValidateAccessToken(r.Context(), token)
		if err == nil {
			forgeCtx = forge.NewContextWithUser(forge.User{
				ID:    at.ClientID,
				Roles: []forge.Role{oauthScopeToRole(at.Scope)},
			})
		} else if s.forgeFallback && errors.Is(err, forgeoauth.ErrTokenNotFound) {
			// Token not found in OAuth store — try forge bearer token as fallback.
			user, ok := forge.VerifyTokenString(token, s.secret, s.tokenStore)
			if !ok {
				s.writeOAuthChallenge(w)
				return
			}
			forgeCtx = forge.NewContextWithUser(user)
		} else {
			s.writeOAuthChallenge(w)
			return
		}
	} else if len(s.secret) > 0 {
		// Forge bearer token mode.
		user, ok := forge.VerifyBearerToken(r, s.secret, s.tokenStore)
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		forgeCtx = forge.NewContextWithUser(user)
	} else {
		// No authentication configured: treat caller as GuestUser.
		forgeCtx = forge.NewContextWithUser(forge.GuestUser)
	}

	// Body limit: protect against large payloads.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "Request Too Large", http.StatusRequestEntityTooLarge)
			return
		}
		resp := jsonRPCResponse{
			JSONRPC: "2.0",
			Error:   &jsonRPCError{Code: -32700, Message: "parse error"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
		return
	}

	resp := s.handle(forgeCtx, req)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

// — OAuth helpers —————————————————————————————————————————————————————————

// extractBearerToken returns the raw token from an "Authorization: Bearer <token>"
// header, or empty string if absent or malformed.
func extractBearerToken(r *http.Request) string {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return token
}

// oauthScopeToRole maps an OAuth scope string to a Forge role.
//
//	"mcp:admin"      → forge.Admin
//	all other values → forge.Author  (standard forge-operator scope for AI clients)
//
// The scope "offline_access" is a modifier (enables refresh tokens) and does
// not affect role mapping — it is combined with the primary scope as a
// space-separated string (e.g. "mcp offline_access" → Author).
func oauthScopeToRole(scope string) forge.Role {
	for _, s := range strings.Fields(scope) {
		if s == "mcp:admin" {
			return forge.Admin
		}
	}
	return forge.Author
}

// writeOAuthChallenge writes HTTP 401 Unauthorized with a WWW-Authenticate
// header pointing to this server's protected resource metadata document
// (RFC 9728). AI clients use this URL to discover the authorization server.
func (s *Server) writeOAuthChallenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate",
		`Bearer resource_metadata="`+s.app.BaseURL()+`/.well-known/oauth-protected-resource"`)
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
}

// protectedResourceHandler serves GET /.well-known/oauth-protected-resource
// (RFC 9728 — OAuth 2.0 Protected Resource Metadata). Returns JSON identifying
// this MCP server as a protected resource and listing its authorization server.
// Returns 404 when OAuth is not enabled ([WithOAuth] not configured).
func (s *Server) protectedResourceHandler(w http.ResponseWriter, r *http.Request) {
	if s.oauth == nil {
		http.NotFound(w, r)
		return
	}
	meta := map[string]any{
		"resource":                 s.app.BaseURL() + "/mcp",
		"authorization_servers":   []string{s.oauth.Issuer()},
		"bearer_methods_supported": []string{"header"},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(meta) //nolint:errcheck
}
