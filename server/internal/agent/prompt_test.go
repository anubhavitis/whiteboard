package agent

import (
	"strings"
	"testing"
)

// The skill must actually reach the model. A typo'd embed path or a botched
// join would leave the agent with a role line and no canvas knowledge, which
// looks like a model regression rather than a build problem.
func TestSystemPromptContainsTheSkill(t *testing.T) {
	if CanvasSkill == "" {
		t.Fatal("canvas skill is empty")
	}
	if !strings.Contains(SystemPrompt, CanvasSkill) {
		t.Error("SystemPrompt does not include the canvas skill")
	}
	if !strings.HasPrefix(SystemPrompt, "You are a thinking partner") {
		t.Error("the role line should come first")
	}
	if !strings.Contains(SystemPrompt, "## How to talk") {
		t.Error("voice guidance is missing")
	}
}

// Each rule below is here because an agent broke it on a real canvas. A rule
// silently dropped while editing the markdown is a regression the Go tests would
// otherwise never see.
//
// These match on a distinctive fragment rather than a whole sentence: the point
// is that the RULE survives an edit, not that its wording is frozen. If a
// reword breaks one of these, update the fragment — do not delete the check.
func TestSkillKeepsTheRulesTheCodeDependsOn(t *testing.T) {
	rules := map[string]string{
		"never emit pixel coordinates":    "never emit pixels",
		"place with near + direction":     "direction",
		"ids are not names":               "Ids are plumbing, never names",
		"unlabeled shapes have no name":   `"unlabeled": true`,
		"cannot know a new id early":      "cannot know a new shape's id",
		"do not recreate on arrow fail":   "already exists",
		"diamond is the decision shape":   "diamond",
		"decision goes upstream":          "before the steps it controls",
		"branches need destinations":      "real destination",
		"insertion removes the old arrow": "delete that arrow",
		"no claiming unmade edits":        "tool call actually did it",
		"structure comes from arrows":     "not coordinates",
	}
	for name, want := range rules {
		if !strings.Contains(CanvasSkill, want) {
			t.Errorf("the skill no longer states %s (looked for %q)", name, want)
		}
	}
}

// Every tool the schema offers should be named in the skill, or the agent has a
// capability nothing told it about.
func TestSkillMentionsEveryTool(t *testing.T) {
	for _, tl := range Tools() {
		if !strings.Contains(CanvasSkill, tl.Name) {
			t.Errorf("the skill never mentions %s", tl.Name)
		}
	}
}

// Every shape the frontend can draw should be named, and none that it cannot.
func TestSkillMatchesTheShapeEnum(t *testing.T) {
	var shapes []string
	for _, tl := range Tools() {
		if tl.Name != "create_shape" {
			continue
		}
		shapes = tl.Schema.Properties["shape"].Enum
	}
	if len(shapes) == 0 {
		t.Fatal("create_shape has no shape enum")
	}
	for _, s := range shapes {
		if !strings.Contains(CanvasSkill, s) {
			t.Errorf("shape %q is offered by the schema but absent from the skill", s)
		}
	}
}

// The prompt is resent every turn and competes with the canvas for context, so a
// large jump should be a deliberate decision rather than a surprise.
func TestPromptStaysWithinItsBudget(t *testing.T) {
	// ~4 chars per token; the canvas budget is 8000 tokens (MAX_CONTEXT_TOKENS).
	const maxChars = 8000
	if n := len(SystemPrompt); n > maxChars {
		t.Errorf("system prompt is %d chars (~%d tokens); it is resent every turn and "+
			"crowds out the canvas. Sharpen a rule rather than adding one, or raise this "+
			"limit deliberately.", n, n/4)
	}
}
