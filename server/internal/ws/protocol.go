// Package ws defines the client/server message protocol and the session loop
// that carries it. Per D4 there is exactly one WebSocket per session, and per
// D5 tool execution happens in the browser, so this protocol is symmetrical:
// the server sends tool calls, the client sends results back.
package ws

import "encoding/json"

// Message types sent by the client.
const (
	TypeUserMessage = "user_message"
	TypeToolResult  = "tool_result"
	TypeCancel      = "cancel"
	TypePing        = "ping"
	TypeSwitchAgent = "switch_agent"
	TypeSetSkills   = "set_skills"
	TypeSaveSkill   = "save_skill"
	TypeDeleteSkill = "delete_skill"
)

// Message types sent by the server.
const (
	TypeAssistantDelta  = "assistant_delta"
	TypeToolCall        = "tool_call"
	TypeTurnEnd         = "turn_end"
	TypeError           = "error"
	TypePong            = "pong"
	TypeAgentsAvailable = "agents_available"
	TypeAgentSwitched   = "agent_switched"
	TypeSkillsState     = "skills_state"
)

// Envelope is the outer frame of every message in both directions. Payload is
// left raw so the receiving side decodes it only once Type is known.
type Envelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// UserMessage is a chat turn from the browser. CanvasContext is the serialized
// canvas as of send time; it is opaque here and interpreted by the agent loop.
type UserMessage struct {
	Text          string          `json:"text"`
	CanvasContext json.RawMessage `json:"canvas_context,omitempty"`
}

// SwitchAgent asks to replace this session's agent (planv2.md §1.2 — the chat
// panel's dropdown is just a string on the wire). Name must be one the server
// offers in AgentsAvailable.
type SwitchAgent struct {
	Name string `json:"name"`
}

// AgentsAvailable is sent once on connect so the UI can render its dropdown
// from what the server actually has, rather than a hardcoded list that can drift.
type AgentsAvailable struct {
	Names   []string `json:"names"`
	Current string   `json:"current"`
}

// SetSkills chooses which optional skills apply to this session. The core canvas
// skill is always active and is not in this list.
type SetSkills struct {
	Enabled []string `json:"enabled"`
}

// SaveSkill creates or replaces a user skill. Built-in ids are rejected.
type SaveSkill struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

// DeleteSkill removes a user skill.
type DeleteSkill struct {
	ID string `json:"id"`
}

// SkillInfo is one skill as the UI sees it. Body is omitted from the list to keep
// the frame small; the UI asks for it only when editing.
type SkillInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	BuiltIn     bool   `json:"built_in"`
	Tokens      int    `json:"tokens"`
	Body        string `json:"body,omitempty"`
}

// SkillsState is the whole picker state: what exists, what is on, and what the
// current selection costs. Sent on connect and after any change, so the UI never
// has to guess.
type SkillsState struct {
	Skills  []SkillInfo `json:"skills"`
	Enabled []string    `json:"enabled"`
	// PromptTokens is the composed prompt's cost, shown so the person can see
	// that skills are resent every turn and compete with the canvas.
	PromptTokens int `json:"prompt_tokens"`
	// CanvasBudget is the token budget the canvas itself gets, for context.
	CanvasBudget int `json:"canvas_budget"`
}

// AgentSwitched confirms a switch. The transcript is not replayed into the new
// agent: each agent keeps its own history, so switching starts a fresh thread.
type AgentSwitched struct {
	Current string `json:"current"`
}

// ToolResult reports the outcome of a tool the browser executed against the
// tldraw store.
type ToolResult struct {
	ID              string   `json:"id"`
	OK              bool     `json:"ok"`
	ResultingShapes []string `json:"resulting_shape_ids,omitempty"`
	Error           string   `json:"error,omitempty"`
}

// AssistantDelta is one streamed chunk of assistant text.
type AssistantDelta struct {
	Text string `json:"text"`
}

// ToolCall asks the browser to apply one tool to the canvas.
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// ErrorPayload surfaces a failure to the UI. Per Phase 5.2 errors are shown,
// never swallowed.
type ErrorPayload struct {
	Message string `json:"message"`
}

func newEnvelope(msgType string, payload any) (Envelope, error) {
	if payload == nil {
		return Envelope{Type: msgType}, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{Type: msgType, Payload: raw}, nil
}
