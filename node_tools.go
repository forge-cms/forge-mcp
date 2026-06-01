package forgemcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"smeldr.dev/core"
)

// nodeToolSet is the set of generic node lifecycle tool names. Used by
// [isNodeTool] to intercept these before the module-scoped tool dispatch so a
// content type can never shadow them.
var nodeToolSet = map[string]bool{
	"create_node":  true,
	"update_node":  true,
	"get_node":     true,
	"list_nodes":   true,
	"publish_node": true,
	"archive_node": true,
}

// isNodeTool reports whether name is one of the generic node lifecycle tools.
func isNodeTool(name string) bool { return nodeToolSet[name] }

// nodeToolDefs returns the six generic node lifecycle tool definitions. They are
// appended to tools/list by [handleToolsList] only when the server has block
// support ([WithBlocks]). All require Author role.
func nodeToolDefs() []mcpTool {
	idOnly := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{"type": "string", "description": "ID of the block."},
		},
		"required": []string{"id"},
	}
	return []mcpTool{
		{
			Name:        "create_node",
			Description: "Create a block (generic content node) as a Draft. type_name is the block-type discriminator (e.g. \"content_block\", \"hero\", \"faq_item\"); fields holds the type-specific data as a JSON object. Requires Author role.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type_name": map[string]any{"type": "string", "description": "Block type, e.g. \"content_block\", \"hero\", \"faq_item\"."},
					"fields":    map[string]any{"type": "object", "description": "Type-specific fields as a JSON object."},
				},
				"required": []string{"type_name"},
			},
		},
		{
			Name:        "update_node",
			Description: "Partially update a block's fields by ID. Keys present in fields are merged onto the stored fields; absent keys are preserved. type_name cannot be changed. Requires Author role.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":     map[string]any{"type": "string", "description": "ID of the block to update."},
					"fields": map[string]any{"type": "object", "description": "Fields to merge onto the stored block."},
				},
				"required": []string{"id"},
			},
		},
		{
			Name:        "get_node",
			Description: "Get a single block by ID, at any lifecycle status. Requires Author role.",
			InputSchema: idOnly,
		},
		{
			Name:        "list_nodes",
			Description: "List blocks, optionally filtered by type_name and/or status, ordered by creation time. Requires Author role.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type_name": map[string]any{"type": "string", "description": "Filter by block type. Omit for all types."},
					"status": map[string]any{
						"type":        "string",
						"enum":        []string{"draft", "scheduled", "published", "archived"},
						"description": "Filter by lifecycle status. Omit for all statuses.",
					},
				},
			},
		},
		{
			Name:        "publish_node",
			Description: "Publish a block by ID. Idempotent if already published. Requires Author role.",
			InputSchema: idOnly,
		},
		{
			Name:        "archive_node",
			Description: "Archive a block by ID. Requires Author role.",
			InputSchema: idOnly,
		},
	}
}

// handleNodeTool dispatches the generic node lifecycle tools. Called only when
// s.blockRepo is non-nil and the caller holds Author role (checked by the caller).
func (s *Server) handleNodeTool(ctx smeldr.Context, name string, args map[string]any) (any, *jsonRPCError) {
	switch name {
	case "create_node":
		typeName, ok := stringArg(args, "type_name")
		if !ok {
			return nil, &jsonRPCError{Code: -32602, Message: "invalid params: type_name required"}
		}
		fields, rpcErr := marshalFields(args["fields"])
		if rpcErr != nil {
			return nil, rpcErr
		}
		node := &smeldr.DynamicNode{
			Node:     smeldr.Node{ID: smeldr.NewID(), Status: smeldr.Draft},
			TypeName: typeName,
			Fields:   fields,
		}
		if err := s.blockRepo.Save(ctx, node); err != nil {
			return nil, errorFor(err)
		}
		return toolResult(nodeSummary(node)), nil

	case "update_node":
		id, ok := stringArg(args, "id")
		if !ok {
			return nil, &jsonRPCError{Code: -32602, Message: "invalid params: id required"}
		}
		node, err := s.blockRepo.FindByID(ctx, id)
		if err != nil {
			return nil, errorFor(err)
		}
		merged, rpcErr := mergeFields(node.Fields, args["fields"])
		if rpcErr != nil {
			return nil, rpcErr
		}
		node.Fields = merged
		if err := s.blockRepo.Save(ctx, node); err != nil {
			return nil, errorFor(err)
		}
		return toolResult(node), nil

	case "get_node":
		id, ok := stringArg(args, "id")
		if !ok {
			return nil, &jsonRPCError{Code: -32602, Message: "invalid params: id required"}
		}
		node, err := s.blockRepo.FindByID(ctx, id)
		if err != nil {
			return nil, errorFor(err)
		}
		return toolResult(node), nil

	case "list_nodes":
		nodes, err := s.listNodes(ctx, args)
		if err != nil {
			return nil, errorFor(err)
		}
		return toolResult(map[string]any{"items": nodes}), nil

	case "publish_node":
		id, ok := stringArg(args, "id")
		if !ok {
			return nil, &jsonRPCError{Code: -32602, Message: "invalid params: id required"}
		}
		node, err := s.blockRepo.FindByID(ctx, id)
		if err != nil {
			return nil, errorFor(err)
		}
		// Idempotent: do not re-stamp PublishedAt if already published.
		if node.Status == smeldr.Published {
			return toolResult(map[string]any{"id": id, "status": "published"}), nil
		}
		node.Status = smeldr.Published
		node.PublishedAt = time.Now().UTC()
		if err := s.blockRepo.Save(ctx, node); err != nil {
			return nil, errorFor(err)
		}
		return toolResult(map[string]any{"id": id, "status": "published"}), nil

	case "archive_node":
		id, ok := stringArg(args, "id")
		if !ok {
			return nil, &jsonRPCError{Code: -32602, Message: "invalid params: id required"}
		}
		node, err := s.blockRepo.FindByID(ctx, id)
		if err != nil {
			return nil, errorFor(err)
		}
		node.Status = smeldr.Archived
		if err := s.blockRepo.Save(ctx, node); err != nil {
			return nil, errorFor(err)
		}
		return toolResult(map[string]any{"id": id, "status": "archived"}), nil
	}
	return nil, &jsonRPCError{Code: -32602, Message: "unknown node tool: " + name}
}

// listNodes runs a filtered SELECT over smeldr_dynamic_content using the core
// generic Query helper, applying optional type_name and status filters.
func (s *Server) listNodes(ctx smeldr.Context, args map[string]any) ([]*smeldr.DynamicNode, error) {
	query := "SELECT * FROM smeldr_dynamic_content"
	var conds []string
	var qargs []any
	n := 1
	if tn, ok := stringArg(args, "type_name"); ok {
		conds = append(conds, fmt.Sprintf("type_name = $%d", n))
		qargs = append(qargs, tn)
		n++
	}
	if st, ok := stringArg(args, "status"); ok {
		conds = append(conds, fmt.Sprintf("status = $%d", n))
		qargs = append(qargs, st)
		n++
	}
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += " ORDER BY created_at"

	nodes, err := smeldr.Query[*smeldr.DynamicNode](ctx, s.app.Config().DB, query, qargs...)
	if err != nil {
		return nil, err
	}
	if nodes == nil {
		nodes = []*smeldr.DynamicNode{}
	}
	return nodes, nil
}

// nodeSummary is the compact response returned by create_node.
func nodeSummary(n *smeldr.DynamicNode) map[string]any {
	return map[string]any{
		"id":        n.ID,
		"type_name": n.TypeName,
		"status":    string(n.Status),
		"slug":      n.Slug,
	}
}

// marshalFields converts the decoded "fields" argument (a JSON object) to
// json.RawMessage. An absent/nil value yields "{}".
func marshalFields(v any) (json.RawMessage, *jsonRPCError) {
	if v == nil {
		return json.RawMessage(`{}`), nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, &jsonRPCError{Code: -32602, Message: "invalid params: fields must be an object"}
	}
	data, err := json.Marshal(m)
	if err != nil {
		return nil, &jsonRPCError{Code: -32602, Message: "invalid params: fields not serialisable: " + err.Error()}
	}
	return data, nil
}

// mergeFields shallow-merges the provided "fields" object onto the stored JSON.
// Keys present in update overwrite; absent keys are preserved. A nil/absent
// update returns the stored value unchanged.
func mergeFields(stored json.RawMessage, update any) (json.RawMessage, *jsonRPCError) {
	if update == nil {
		return stored, nil
	}
	upd, ok := update.(map[string]any)
	if !ok {
		return nil, &jsonRPCError{Code: -32602, Message: "invalid params: fields must be an object"}
	}
	base := map[string]any{}
	if len(stored) > 0 {
		// If the stored value is not a JSON object, start from an empty base.
		_ = json.Unmarshal(stored, &base)
	}
	for k, v := range upd {
		base[k] = v
	}
	data, err := json.Marshal(base)
	if err != nil {
		return nil, &jsonRPCError{Code: -32602, Message: "invalid params: merge failed: " + err.Error()}
	}
	return data, nil
}
