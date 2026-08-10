// Package agent defines the boundary between the whiteboard and whatever is
// thinking for it.
//
// The two agents run their loops in OPPOSITE places (planv2.md §"The core
// abstraction"):
//
//   - Claude Code owns its own agentic loop; tool calls arrive *into* this
//     process via an MCP callback.
//   - A native agent (MLX/Qwen3 today, an API key later) has its loop *here*;
//     tool calls are parsed out of a streamed response.
//
// So the abstraction sits above the loop, not at the model-API level. Both
// implementations push into one event channel, and a single dispatcher handles
// every tool call identically.
//
// THE IMPORT RULE: nothing outside an AgentSession implementation may import
// anthropic/openai/mcp/exec packages. That rule is the whole "swap in a local
// model or an API key later" guarantee — keep it.
package agent

import (
	"context"
	"encoding/json"
)

// EventType discriminates AgentEvent.
type EventType string

const (
	// EventTextDelta carries a chunk of assistant text for the UI.
	EventTextDelta EventType = "text_delta"
	// EventToolCall asks for a canvas tool to be executed.
	EventToolCall EventType = "tool_call"
	// EventToolDone reports a tool's outcome, for UI feedback only — the
	// result is returned to the agent through the dispatcher, not this event.
	EventToolDone EventType = "tool_done"
	// EventTurnDone marks the end of one assistant turn.
	EventTurnDone EventType = "turn_done"
	// EventError carries a failure worth showing the user (planv2.md §5.2).
	EventError EventType = "error"
)

// AgentEvent is the single event shape both agents emit.
type AgentEvent struct {
	Type EventType

	// Text is set on EventTextDelta and EventError.
	Text string

	// Tool fields are set on EventToolCall and EventToolDone.
	ToolID   string
	ToolName string
	ToolArgs json.RawMessage
	ToolOK   bool
}

// ToolExecutor runs a canvas tool and returns its result. The browser is the
// real implementation (D5); the agent never learns how execution happens.
//
// Implementations must be safe for concurrent use: Claude Code can request
// several tools at once.
type ToolExecutor interface {
	Execute(ctx context.Context, call ToolInvocation) ToolOutcome
}

// ToolInvocation is one tool the agent wants run.
type ToolInvocation struct {
	ID   string
	Name string
	Args json.RawMessage
}

// ToolOutcome is what came back from the canvas.
type ToolOutcome struct {
	OK       bool
	ShapeIDs []string
	Error    string
}

// AgentSession is one live conversation with one agent.
//
// SendTurn must not block for the whole turn: it queues the turn and returns,
// with progress arriving on Events. Close must be safe to call twice.
type AgentSession interface {
	SendTurn(ctx context.Context, text string, canvas json.RawMessage) error
	Events() <-chan AgentEvent
	Close() error
}

// Factory builds a session for a given agent choice. The chat panel's dropdown
// is just a string on the wire (planv2.md §1.2), and this is where it lands.
type Factory interface {
	// New starts a session. sessionID is the whiteboard's own id, used by the
	// Claude Code implementation to route MCP callbacks.
	New(ctx context.Context, sessionID string, exec ToolExecutor) (AgentSession, error)
	// Name is the value the dropdown sends.
	Name() string
}
