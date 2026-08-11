package agent

// Tool schema v1 — four tools, defined ONCE (planv2.md §2.1). This file renders
// to both MCP tool registrations and plain JSON Schema for a native loop, so
// the two agents can never drift apart on what a tool means.
//
// Note what is absent: no x, no y, no coordinates of any kind. The model places
// shapes *relative* to existing ones and the frontend computes pixels. LLMs are
// bad at absolute coordinates, and this is the documented failure mode of every
// canvas-agent project (DECISIONS.md, planv2.md §2.2).

// MaxToolCallsPerTurn bounds a native agent's loop. Claude Code enforces its
// own cap via --max-turns; this is for the loop we own.
const MaxToolCallsPerTurn = 15

// Tool is one canvas capability.
type Tool struct {
	Name        string
	Description string
	Schema      Schema
}

// Schema is the JSON Schema subset both surfaces need.
type Schema struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties"`
	Required   []string            `json:"required"`
	// Additional is always false: strict schemas keep the model from inventing
	// coordinate parameters.
	Additional bool `json:"additionalProperties"`
}

// Property is one input field.
type Property struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Enum        []string `json:"enum,omitempty"`
}

// Tools returns the canonical tool set.
func Tools() []Tool {
	return []Tool{
		{
			Name: "create_shape",
			Description: "Create a new shape on the whiteboard. Position it relative to an existing " +
				"shape with `near` and `direction`; omit both only when the canvas is empty.",
			Schema: Schema{
				Type: "object",
				Properties: map[string]Property{
					"shape": {
						Type:        "string",
						Description: "The kind of shape to draw.",
						Enum:        []string{"box", "ellipse", "text"},
					},
					"text": {
						Type:        "string",
						Description: "The label shown on the shape. Keep it short — a few words.",
					},
					"near": {
						Type:        "string",
						Description: "Id of an existing shape to position this one against. Omit only for the first shape on an empty canvas.",
					},
					"direction": {
						Type:        "string",
						Description: "Where to place the new shape relative to `near`. Required whenever `near` is given.",
						Enum:        []string{"above", "below", "left_of", "right_of"},
					},
				},
				Required:   []string{"shape", "text"},
				Additional: false,
			},
		},
		{
			Name:        "create_arrow",
			Description: "Draw an arrow between two existing shapes, showing a dependency or flow.",
			Schema: Schema{
				Type: "object",
				Properties: map[string]Property{
					"from_id": {Type: "string", Description: "Id of the shape the arrow starts at."},
					"to_id":   {Type: "string", Description: "Id of the shape the arrow points to."},
					"text":    {Type: "string", Description: "Optional short label for the arrow."},
				},
				Required:   []string{"from_id", "to_id"},
				Additional: false,
			},
		},
		{
			Name:        "update_shape",
			Description: "Change the label or colour of an existing shape. Prefer this over deleting and recreating.",
			Schema: Schema{
				Type: "object",
				Properties: map[string]Property{
					"id":   {Type: "string", Description: "Id of the shape to change."},
					"text": {Type: "string", Description: "New label for the shape."},
					"color": {
						Type:        "string",
						Description: "New colour for the shape.",
						Enum:        []string{"black", "blue", "green", "orange", "red", "violet"},
					},
				},
				Required:   []string{"id"},
				Additional: false,
			},
		},
		{
			Name:        "delete_shape",
			Description: "Remove a shape from the canvas. Arrows bound to it are removed too.",
			Schema: Schema{
				Type: "object",
				Properties: map[string]Property{
					"id": {Type: "string", Description: "Id of the shape to delete."},
				},
				Required:   []string{"id"},
				Additional: false,
			},
		},
	}
}

// MCPTools renders the tool set as MCP `tools/list` entries.
func MCPTools() []map[string]any {
	tools := Tools()
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.Schema.toJSON(),
		})
	}
	return out
}

// OpenAITools renders the tool set for an OpenAI-compatible endpoint, which is
// what a native loop talks to (mlx_lm.server today, any compatible server later).
//
// The nesting matters and is not cosmetic: mlx_lm hands `tools` straight to the
// model's Jinja chat template, and Qwen3's template reads `.function.name` and
// `.function.parameters`. A flat {name, description, input_schema} object — the
// Anthropic shape — templates to nothing, and mlx_lm does not complain: the
// model simply never learns the tools exist. This is the exact shape spike S3
// scored 10/10 against (spikes/FINDINGS.md).
func OpenAITools() []map[string]any {
	tools := Tools()
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Schema.toJSON(),
			},
		})
	}
	return out
}

// AnthropicTools renders the tool set for an Anthropic-style API, where the
// schema key is `input_schema` and there is no `function` wrapper. Nothing calls
// this yet; it exists so the "schemas defined once" claim survives the day an
// API key path appears.
func AnthropicTools() []map[string]any {
	tools := Tools()
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": t.Schema.toJSON(),
		})
	}
	return out
}

func (s Schema) toJSON() map[string]any {
	props := make(map[string]any, len(s.Properties))
	for name, p := range s.Properties {
		entry := map[string]any{"type": p.Type, "description": p.Description}
		if len(p.Enum) > 0 {
			entry["enum"] = p.Enum
		}
		props[name] = entry
	}
	return map[string]any{
		"type":                 s.Type,
		"properties":           props,
		"required":             s.Required,
		"additionalProperties": s.Additional,
	}
}
