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
	// Uses the production renderer rather than hand-building the shape, so the
	// spike can never score the model against schemas we do not actually send.
	out, err := json.MarshalIndent(map[string]any{
		"tools":         OpenAITools(),
		"system_prompt": SystemPrompt,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("testdata/openai_tools.json", append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFixture() ([]byte, error) {
	return os.ReadFile("testdata/openai_tools.json")
}
