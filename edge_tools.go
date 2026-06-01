package forgemcp

import "smeldr.dev/core"

// compositionToolSet is the set of composition (edge) tool names. Used by
// [isCompositionTool] to intercept these before the module-scoped tool dispatch.
var compositionToolSet = map[string]bool{
	"add_section":      true,
	"reorder_sections": true,
	"remove_section":   true,
	"add_item":         true,
	"reorder_items":    true,
	"remove_item":      true,
}

// isCompositionTool reports whether name is one of the composition tools.
func isCompositionTool(name string) bool { return compositionToolSet[name] }

// compositionToolDefs returns the six composition tool definitions: three for
// page sections (edge_role "section") and three for collection items
// (edge_role "item"). Appended to tools/list by [handleToolsList] only when the
// server has block support ([WithBlocks]). All require Editor role.
//
// Sections and items are deliberately distinct tools for operator clarity
// ("add a section to the page" vs. "add an item to the collection"); they share
// one implementation parameterised by edge_role.
func compositionToolDefs() []mcpTool {
	parentChild := func(action string) map[string]any {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"parent_id": map[string]any{"type": "string", "description": "ID of the parent block (the page or collection)."},
				"child_id":  map[string]any{"type": "string", "description": "ID of the child block to " + action + "."},
			},
			"required": []string{"parent_id", "child_id"},
		}
	}
	reorder := func(kind string) map[string]any {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"parent_id": map[string]any{"type": "string", "description": "ID of the parent block."},
				"ordered_child_ids": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Child IDs in the desired " + kind + " order (first ID becomes position 0).",
				},
			},
			"required": []string{"parent_id", "ordered_child_ids"},
		}
	}
	return []mcpTool{
		{Name: "add_section", Description: "Append a block as the last section of a page. Requires Editor role.", InputSchema: parentChild("add as a section")},
		{Name: "reorder_sections", Description: "Reorder a page's sections to match the given order. Requires Editor role.", InputSchema: reorder("section")},
		{Name: "remove_section", Description: "Remove a section from a page. Requires Editor role.", InputSchema: parentChild("remove")},
		{Name: "add_item", Description: "Append a block as the last item of a collection. Requires Editor role.", InputSchema: parentChild("add as an item")},
		{Name: "reorder_items", Description: "Reorder a collection's items to match the given order. Requires Editor role.", InputSchema: reorder("item")},
		{Name: "remove_item", Description: "Remove an item from a collection. Requires Editor role.", InputSchema: parentChild("remove")},
	}
}

// handleCompositionTool dispatches the composition tools. Called only when
// s.blockRepo is non-nil and the caller holds Editor role (checked by the caller).
func (s *Server) handleCompositionTool(ctx smeldr.Context, name string, args map[string]any) (any, *jsonRPCError) {
	switch name {
	case "add_section":
		return s.addEdge(ctx, args, "section")
	case "add_item":
		return s.addEdge(ctx, args, "item")
	case "reorder_sections", "reorder_items":
		return s.reorderEdges(ctx, args)
	case "remove_section", "remove_item":
		return s.removeEdge(ctx, args)
	}
	return nil, &jsonRPCError{Code: -32602, Message: "unknown composition tool: " + name}
}

// addEdge appends child_id under parent_id with the given edge role. The
// parent_type and child_type are derived from the stored blocks' type_name, so
// the operator never has to pass them — and they can never be mismatched.
func (s *Server) addEdge(ctx smeldr.Context, args map[string]any, role string) (any, *jsonRPCError) {
	parentID, ok := stringArg(args, "parent_id")
	if !ok {
		return nil, &jsonRPCError{Code: -32602, Message: "invalid params: parent_id required"}
	}
	childID, ok := stringArg(args, "child_id")
	if !ok {
		return nil, &jsonRPCError{Code: -32602, Message: "invalid params: child_id required"}
	}
	parent, err := s.blockRepo.FindByID(ctx, parentID)
	if err != nil {
		return nil, errorFor(err)
	}
	child, err := s.blockRepo.FindByID(ctx, childID)
	if err != nil {
		return nil, errorFor(err)
	}
	edge, err := s.edgeStore.AddChild(ctx, smeldr.ContentEdge{
		ParentID:   parentID,
		ParentType: parent.TypeName,
		ChildID:    childID,
		ChildType:  child.TypeName,
		EdgeRole:   role,
	})
	if err != nil {
		return nil, errorFor(err)
	}
	return toolResult(edge), nil
}

// reorderEdges sets the order of a parent's children to ordered_child_ids.
func (s *Server) reorderEdges(ctx smeldr.Context, args map[string]any) (any, *jsonRPCError) {
	parentID, ok := stringArg(args, "parent_id")
	if !ok {
		return nil, &jsonRPCError{Code: -32602, Message: "invalid params: parent_id required"}
	}
	ids, rpcErr := stringSliceArg(args, "ordered_child_ids")
	if rpcErr != nil {
		return nil, rpcErr
	}
	if err := s.edgeStore.Reorder(ctx, parentID, ids); err != nil {
		return nil, errorFor(err)
	}
	return toolResult(map[string]any{"parent_id": parentID, "ordered_child_ids": ids}), nil
}

// removeEdge deletes the edge linking parent_id to child_id.
func (s *Server) removeEdge(ctx smeldr.Context, args map[string]any) (any, *jsonRPCError) {
	parentID, ok := stringArg(args, "parent_id")
	if !ok {
		return nil, &jsonRPCError{Code: -32602, Message: "invalid params: parent_id required"}
	}
	childID, ok := stringArg(args, "child_id")
	if !ok {
		return nil, &jsonRPCError{Code: -32602, Message: "invalid params: child_id required"}
	}
	if err := s.edgeStore.RemoveChild(ctx, parentID, childID); err != nil {
		return nil, errorFor(err)
	}
	return toolResult(map[string]any{"removed": true, "parent_id": parentID, "child_id": childID}), nil
}

// stringSliceArg extracts a non-empty []string from a JSON array argument.
func stringSliceArg(args map[string]any, key string) ([]string, *jsonRPCError) {
	v, ok := args[key]
	if !ok {
		return nil, &jsonRPCError{Code: -32602, Message: "invalid params: " + key + " required"}
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, &jsonRPCError{Code: -32602, Message: "invalid params: " + key + " must be an array of strings"}
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		str, ok := e.(string)
		if !ok {
			return nil, &jsonRPCError{Code: -32602, Message: "invalid params: " + key + " must contain only strings"}
		}
		out = append(out, str)
	}
	if len(out) == 0 {
		return nil, &jsonRPCError{Code: -32602, Message: "invalid params: " + key + " must not be empty"}
	}
	return out, nil
}
