package agent

import (
	"embed"
	"strings"
)

// canvasSkill holds the canvas skill as a real markdown file rather than a Go
// string literal.
//
// The reason is maintenance, not elegance: nearly every rule in it was added
// after an agent got something wrong on a real canvas, and that kind of content
// wants to be reviewable in a diff and editable without touching code. Embedding
// keeps it a single binary with no runtime file lookup.
//
//go:embed canvas_skill.md
var canvasSkill embed.FS

// CanvasSkill is the shared drawing and canvas-reading guidance, identical for
// every agent: Claude Code receives it via --append-system-prompt, a native agent
// as its system message. One source, so the two can never drift.
var CanvasSkill = mustRead("canvas_skill.md")

// RolePreamble and VoiceGuidance bracket the skills in a composed prompt. They
// are separate from the skill text so a session can swap which skills apply
// without rebuilding the parts that never change.
const RolePreamble = `You are a thinking partner working alongside someone on an infinite whiteboard canvas.`

const VoiceGuidance = `## How to talk

Keep answers short. Two or three sentences of prose for an ordinary question. Reach for a list only when the person asked for one or the canvas genuinely holds a list of things; never nest bullets inside bullets, and never enumerate shapes one by one when a sentence covers it — "a seven-step tea flow, plus two unlabelled boxes off to the side" beats seven bullet points.

Be direct and concrete. When you see a gap, a missing dependency, or a contradiction, say it plainly and say why. Skip the preamble: no "Great question!", no restating what they drew. If the canvas is empty, say so and ask what they want to think through.`

// ComposePrompt builds the full system prompt for a session: role, then the
// canvas skill plus whichever optional skills are enabled, then voice.
//
// skills is the already-composed skill text (see SkillStore.Compose), which
// always begins with the core canvas skill.
func ComposePrompt(skills string) string {
	return strings.Join([]string{RolePreamble, skills, VoiceGuidance}, "\n\n")
}

// SystemPrompt is what an agent is actually given: who it is, then the canvas
// skill, then how to talk.
//
// The split is deliberate. The skill is *what the agent knows about the canvas*
// and lives in markdown. Voice is a handful of lines that only make sense next to
// the role, so they stay here.
//
// Length is a real cost, not a style question: this text and the tool schemas are
// resent on every turn, and together they run ~1,700 tokens against a canvas
// budget of 8,000. Anything added here is taken away from the canvas, which is
// the part carrying the actual information. Prefer sharpening a rule to adding
// one.
var SystemPrompt = ComposePrompt(CanvasSkill)

func mustRead(name string) string {
	b, err := canvasSkill.ReadFile(name)
	if err != nil {
		// Unreachable: go:embed fails the build if the file is missing.
		panic("canvas skill missing from the binary: " + err.Error())
	}
	return strings.TrimSpace(string(b))
}
