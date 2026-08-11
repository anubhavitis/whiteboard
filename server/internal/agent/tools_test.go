package agent

import (
	"encoding/json"
	"testing"
)

// The nesting is load-bearing: mlx_lm passes `tools` to the model's Jinja chat
// template, which reads .function.name and .function.parameters. A flat object
// templates to nothing and the model never learns the tools exist — silently.
func TestOpenAIToolsShape(t *testing.T) {
	tools := OpenAITools()
	if len(tools) != len(Tools()) {
		t.Fatalf("got %d tools, want %d", len(tools), len(Tools()))
	}

	names := map[string]bool{}
	for _, tl := range tools {
		if tl["type"] != "function" {
			t.Errorf(`type = %v, want "function"`, tl["type"])
		}
		fn, ok := tl["function"].(map[string]any)
		if !ok {
			t.Fatalf("function is %T, want a nested object", tl["function"])
		}
		name, _ := fn["name"].(string)
		if name == "" {
			t.Error("function.name is empty")
		}
		names[name] = true
		if desc, _ := fn["description"].(string); desc == "" {
			t.Errorf("%s: function.description is empty", name)
		}
		params, ok := fn["parameters"].(map[string]any)
		if !ok {
			t.Fatalf("%s: function.parameters is %T, want an object", name, fn["parameters"])
		}
		if params["type"] != "object" {
			t.Errorf("%s: parameters.type = %v", name, params["type"])
		}
		if _, ok := params["properties"]; !ok {
			t.Errorf("%s: parameters.properties missing", name)
		}
		// The Anthropic key must not leak into the OpenAI shape.
		if _, bad := fn["input_schema"]; bad {
			t.Errorf("%s: input_schema present in the OpenAI shape", name)
		}
	}

	for _, want := range []string{"create_shape", "create_arrow", "update_shape", "delete_shape"} {
		if !names[want] {
			t.Errorf("missing tool %q", want)
		}
	}
}

// No tool may accept coordinates: the model never places shapes in pixels
// (planv2 §2.2). This asserts the schema, which is what the model is told.
func TestNoToolAcceptsCoordinates(t *testing.T) {
	for _, tl := range OpenAITools() {
		fn := tl["function"].(map[string]any)
		params := fn["parameters"].(map[string]any)
		props, _ := params["properties"].(map[string]any)
		for _, banned := range []string{"x", "y", "w", "h", "width", "height"} {
			if _, found := props[banned]; found {
				t.Errorf("%s exposes %q; positioning must stay relative", fn["name"], banned)
			}
		}
		if params["additionalProperties"] != false {
			t.Errorf("%s: additionalProperties should be false", fn["name"])
		}
	}
}

func TestAnthropicToolsKeepsInputSchema(t *testing.T) {
	for _, tl := range AnthropicTools() {
		if _, ok := tl["input_schema"]; !ok {
			t.Errorf("%v: input_schema missing from the Anthropic shape", tl["name"])
		}
		if _, bad := tl["function"]; bad {
			t.Error("Anthropic shape must not nest under function")
		}
	}
}

// The fixture the S3 spike scores against must stay in sync with what we send.
func TestFixtureMatchesOpenAITools(t *testing.T) {
	raw, err := readFixture()
	if err != nil {
		t.Skipf("fixture not generated: %v", err)
	}
	var got struct {
		Tools        []map[string]any `json:"tools"`
		SystemPrompt string           `json:"system_prompt"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(OpenAITools())
	have, _ := json.Marshal(got.Tools)
	if string(want) != string(have) {
		t.Error("testdata/openai_tools.json is stale; regenerate with:\n" +
			"  DUMP_SCHEMAS=1 go test ./internal/agent -run TestDumpOpenAISchemas")
	}
	if got.SystemPrompt != SystemPrompt {
		t.Error("fixture system_prompt is stale; regenerate it")
	}
}
