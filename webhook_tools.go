package forgemcp

import (
	"fmt"

	"forge-cms.dev/forge"
)

// webhookToolDefs returns the five Admin-only webhook management tool
// definitions:
//   - create_webhook   — register a new endpoint subscription
//   - list_webhooks    — list endpoints with delivery statistics
//   - delete_webhook   — remove an endpoint
//   - list_webhook_deliveries — inspect delivery log for a job or endpoint
//   - retry_webhook    — manually retry a dead-lettered job
func webhookToolDefs() []mcpTool {
	return []mcpTool{
		{
			Name:        "create_webhook",
			Description: "Register a new outbound webhook endpoint. Returns the plaintext signing secret once — copy it immediately. Requires Admin role.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "HTTPS target URL to deliver events to. Must not be a private or localhost address.",
					},
					"events": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": `Event names to subscribe to, e.g. ["post.published", "post.created"]. Supported suffixes: created, updated, published, scheduled, archived, deleted.`,
					},
				},
				"required": []string{"url", "events"},
			},
		},
		{
			Name:        "list_webhooks",
			Description: "List all registered webhook endpoints with delivery statistics. Requires Admin role. Secrets are never returned.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "delete_webhook",
			Description: "Permanently delete a webhook endpoint by ID. Requires Admin role.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "ID of the webhook endpoint to delete.",
					},
				},
				"required": []string{"id"},
			},
		},
		{
			Name:        "list_webhook_deliveries",
			Description: "List delivery logs for a webhook job or all jobs for an endpoint. Provide either job_id or endpoint_id. Requires Admin role.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"job_id": map[string]any{
						"type":        "string",
						"description": "ID of the specific job to inspect delivery logs for.",
					},
					"endpoint_id": map[string]any{
						"type":        "string",
						"description": "ID of the endpoint — returns all jobs for that endpoint.",
					},
				},
			},
		},
		{
			Name:        "retry_webhook",
			Description: "Manually retry a dead-lettered webhook job. Resets attempts to zero and re-queues for delivery. Requires Admin role.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"job_id": map[string]any{
						"type":        "string",
						"description": "ID of the dead-lettered job to retry.",
					},
				},
				"required": []string{"job_id"},
			},
		},
	}
}

// handleWebhookTool dispatches create_webhook, list_webhooks, delete_webhook,
// list_webhook_deliveries, and retry_webhook requests. Called only when
// s.webhookStore is non-nil and the caller holds Admin role (checked by caller).
func (s *Server) handleWebhookTool(ctx forge.Context, name string, args map[string]any) (any, *jsonRPCError) {
	switch name {
	case "create_webhook":
		rawURL, ok := stringArg(args, "url")
		if !ok || rawURL == "" {
			return nil, &jsonRPCError{Code: -32602, Message: "invalid params: url required"}
		}
		eventsRaw, ok := args["events"]
		if !ok {
			return nil, &jsonRPCError{Code: -32602, Message: "invalid params: events required"}
		}
		var events []string
		switch ev := eventsRaw.(type) {
		case []any:
			for _, e := range ev {
				if s, ok := e.(string); ok && s != "" {
					events = append(events, s)
				}
			}
		case []string:
			events = ev
		}
		if len(events) == 0 {
			return nil, &jsonRPCError{Code: -32602, Message: "invalid params: events must be a non-empty array of strings"}
		}
		ep, secret, err := s.webhookStore.Create(ctx, rawURL, events)
		if err != nil {
			return nil, errorFor(err)
		}
		return toolResult(map[string]any{
			"id":      ep.ID,
			"url":     ep.TargetURL,
			"events":  ep.Events,
			"secret":  secret,
			"message": "Store this signing secret securely — it cannot be retrieved again.",
		}), nil

	case "list_webhooks":
		endpoints, err := s.webhookStore.List(ctx)
		if err != nil {
			return nil, errorFor(err)
		}
		if endpoints == nil {
			endpoints = []forge.WebhookEndpoint{}
		}
		// Augment with delivery stats when the pool is available.
		type endpointSummary struct {
			ID         string   `json:"id"`
			TargetURL  string   `json:"target_url"`
			Events     []string `json:"events"`
			Active     bool     `json:"active"`
			CreatedAt  string   `json:"created_at"`
			TotalJobs  int      `json:"total_jobs"`
			Successful int      `json:"successful"`
			Failed     int      `json:"failed"`
			LastAt     string   `json:"last_attempt_at,omitempty"`
		}
		summaries := make([]endpointSummary, 0, len(endpoints))
		for _, ep := range endpoints {
			es := endpointSummary{
				ID:        ep.ID,
				TargetURL: ep.TargetURL,
				Events:    ep.Events,
				Active:    ep.Active,
				CreatedAt: ep.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			}
			if s.webhookPool != nil {
				total, succ, fail, lastAt, _ := s.webhookPool.DeliveryStats(ctx, ep.ID)
				es.TotalJobs = total
				es.Successful = succ
				es.Failed = fail
				if lastAt != nil {
					es.LastAt = lastAt.UTC().Format("2006-01-02T15:04:05Z")
				}
			}
			summaries = append(summaries, es)
		}
		return toolResult(map[string]any{"webhooks": summaries}), nil

	case "delete_webhook":
		id, ok := stringArg(args, "id")
		if !ok || id == "" {
			return nil, &jsonRPCError{Code: -32602, Message: "invalid params: id required"}
		}
		if err := s.webhookStore.Delete(ctx, id); err != nil {
			return nil, errorFor(err)
		}
		return toolResult(map[string]any{"deleted": true, "id": id}), nil

	case "list_webhook_deliveries":
		if s.webhookPool == nil {
			return nil, &jsonRPCError{Code: -32603, Message: "webhook pool not configured"}
		}
		jobID, hasJob := stringArg(args, "job_id")
		endpointID, hasEp := stringArg(args, "endpoint_id")
		if !hasJob && !hasEp {
			return nil, &jsonRPCError{Code: -32602, Message: "invalid params: provide job_id or endpoint_id"}
		}
		if hasJob && jobID != "" {
			logs, err := s.webhookPool.ListDeliveryLogs(ctx, jobID)
			if err != nil {
				return nil, errorFor(err)
			}
			if logs == nil {
				logs = []forge.DeliveryLog{}
			}
			return toolResult(map[string]any{"logs": logs}), nil
		}
		jobs, err := s.webhookPool.ListJobsForEndpoint(ctx, endpointID)
		if err != nil {
			return nil, errorFor(err)
		}
		if jobs == nil {
			jobs = []forge.OutboundJob{}
		}
		return toolResult(map[string]any{"jobs": jobs}), nil

	case "retry_webhook":
		if s.webhookPool == nil {
			return nil, &jsonRPCError{Code: -32603, Message: "webhook pool not configured"}
		}
		jobID, ok := stringArg(args, "job_id")
		if !ok || jobID == "" {
			return nil, &jsonRPCError{Code: -32602, Message: "invalid params: job_id required"}
		}
		if err := s.webhookPool.RetryDead(ctx, jobID); err != nil {
			return nil, errorFor(err)
		}
		return toolResult(map[string]any{"retried": true, "job_id": jobID}), nil

	default:
		return nil, &jsonRPCError{Code: -32602, Message: fmt.Sprintf("unknown webhook tool: %s", name)}
	}
}

// isWebhookTool reports whether name is one of the five webhook admin tools.
func isWebhookTool(name string) bool {
	return name == "create_webhook" ||
		name == "list_webhooks" ||
		name == "delete_webhook" ||
		name == "list_webhook_deliveries" ||
		name == "retry_webhook"
}
