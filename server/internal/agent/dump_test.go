package agent

import (
	"encoding/json"
	"os"
	"testing"
)

// TestDumpOpenAISchemas is a helper, not an assertion: run it with -run to
// regenerate the fixture the S3 spike scores against, so the spike never uses a
// retyped copy of the tool schemas.
func TestDumpOpenAISchemas(t *testing.T) {
	if os.Getenv("DUMP_SCHEMAS") == "" {
		t.Skip("set DUMP_SCHEMAS=1 to regenerate testdata/openai_tools.json")
	}
	tools := make([]map[string]any, 0, 4)
	for _, tl := range Tools() {
		var params map[string]any
		raw, err := json.Marshal(tl.Schema)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &params); err != nil {
			t.Fatal(err)
		}
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tl.Name,
				"description": tl.Description,
				"parameters":  params,
			},
		})
	}
	out, err := json.MarshalIndent(map[string]any{
		"tools":         tools,
		"system_prompt": SystemPrompt,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("testdata/openai_tools.json", append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
