package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anubhavitis/whiteboard/server/internal/agent"
)

// turn runs one user message to completion: N model calls interleaved with N
// tool round trips through the browser.
//
// This is the half of the design that Claude Code does not need. There, the loop
// lives inside the subprocess and tool calls arrive over MCP; here we own it,
// which is why agent.AgentSession sits above the loop rather than at the
// model-API level.
type turn struct {
	sess  *Session
	convo []message // working slice: durable history + this turn's tool traffic
	calls int       // tool calls executed, against agent.MaxToolCallsPerTurn
	// seen counts identical (name, args) pairs, to break a model that is stuck
	// repeating one idea.
	seen map[string]int
	// madeIDs are the shapes created during this turn, in order. They are the
	// ids a guessed reference almost certainly meant.
	madeIDs []string
}

// created returns the shape ids this turn has made so far.
func (t *turn) created() []string {
	return t.madeIDs
}

// repeatLimit is how many times the same call may be made before the loop tells
// the model to stop. Two identical attempts can be legitimate (a transient
// browser error); three is a stuck model burning the call budget.
const repeatLimit = 3

func newTurn(s *Session, convo []message) *turn {
	return &turn{sess: s, convo: convo, seen: map[string]int{}}
}

// run drives the loop. It always ends with exactly one EventTurnDone.
func (t *turn) run(ctx context.Context) {
	defer t.sess.emit(agent.AgentEvent{Type: agent.EventTurnDone})

	var lastText string
	for {
		if ctx.Err() != nil {
			// Cancellation is the Stop button, not a failure to report.
			return
		}

		tools := t.sess.tools()
		// Past the cap, ask for a closing sentence with tools withheld. Omitting
		// them is the enforcement; an instruction to stop is not.
		capped := t.calls >= agent.MaxToolCallsPerTurn
		if capped {
			tools = nil
		}

		res, err := t.sess.client.complete(ctx, t.convo, tools, func(delta string) {
			t.sess.emit(agent.AgentEvent{Type: agent.EventTextDelta, Text: delta})
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			t.sess.log.Error("native turn failed", "err", err)
			t.sess.emit(agent.AgentEvent{
				Type: agent.EventError,
				Text: "the local model could not be reached: " + err.Error(),
			})
			return
		}

		if res.Text != "" {
			lastText = res.Text
		}

		// mlx_lm drops a truncated tool call but still reports finish_reason
		// tool_calls (server.py:82-87). Without this the turn would look like it
		// simply ended, with nothing drawn and nothing said.
		if len(res.Calls) == 0 && res.FinishReason == "tool_calls" {
			t.sess.emit(agent.AgentEvent{
				Type: agent.EventError,
				Text: "the model's tool call was cut off before it finished — try a shorter request",
			})
			t.commit(lastText)
			return
		}

		if len(res.Calls) == 0 {
			// A response with no text and no calls, after only thinking, is the
			// model spending its budget without answering.
			if res.Text == "" && res.SawReasoning {
				t.sess.emit(agent.AgentEvent{
					Type: agent.EventError,
					Text: "the local model spent its whole response thinking and never answered — try a shorter question",
				})
			}
			t.commit(lastText)
			return
		}

		if capped {
			// Tools were withheld yet the model still tried: nothing more to do.
			t.commit(lastText)
			return
		}

		// Replay the assistant turn verbatim, including empty content: an
		// assistant message that is only tool calls is the normal case.
		t.convo = append(t.convo, message{
			Role:      "assistant",
			Content:   res.Text,
			ToolCalls: wire(res.Calls),
		})

		for _, call := range res.Calls {
			t.convo = append(t.convo, t.execute(ctx, call))
			t.calls++
			if t.calls >= agent.MaxToolCallsPerTurn {
				t.sess.emit(agent.AgentEvent{
					Type: agent.EventError,
					Text: fmt.Sprintf("stopped after %d canvas actions in one turn", agent.MaxToolCallsPerTurn),
				})
				break
			}
		}
	}
}

// commit stores the assistant's final text in the durable history.
//
// Tool-call and tool-result messages are deliberately NOT kept: the canvas JSON
// is re-sent with every user message and is the authority on what exists, so
// replaying last turn's tool traffic is pure token cost — and canvas JSON already
// tokenizes ~2.2x worse than a naive estimate (spikes/FINDINGS.md). The cost is
// that the model does not remember "I tried that and it failed"; the canvas shows
// the outcome instead.
//
// The stored assistant message must carry no ToolCalls field: an assistant
// tool_calls with no matching tool result is invalid on the wire.
func (t *turn) commit(text string) {
	if text == "" {
		return
	}
	t.sess.mu.Lock()
	t.sess.history = append(t.sess.history, message{Role: "assistant", Content: text})
	t.sess.mu.Unlock()
}

// execute runs one tool call and renders the result as a role:"tool" message.
func (t *turn) execute(ctx context.Context, call toolCall) message {
	reply := func(v any) message {
		body, err := json.Marshal(v)
		if err != nil {
			body = []byte(`{"ok":false,"error":"could not encode the result"}`)
		}
		return message{
			Role:       "tool",
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    string(body),
		}
	}

	if call.Malformed {
		// The model can fix this itself, so it is a tool error rather than a
		// session error.
		t.sess.log.Warn("native: malformed tool arguments", "tool", call.Name, "args", call.ArgsRaw)
		return reply(map[string]any{"ok": false, "error": "arguments were not valid JSON"})
	}
	if !toolNames()[call.Name] {
		return reply(map[string]any{
			"ok":    false,
			"error": "unknown tool: " + call.Name,
		})
	}

	// Break a model that is stuck on one idea before it burns the whole budget.
	key := call.Name + "|" + string(call.Args)
	t.seen[key]++
	if t.seen[key] >= repeatLimit {
		return reply(map[string]any{
			"ok":    false,
			"error": "you already made this exact call and it did not work — stop and explain instead",
		})
	}

	args, warn := sanitizeArgs(call.Args)
	if warn != "" {
		t.sess.log.Warn("native: model emitted forbidden argument", "tool", call.Name, "warn", warn)
	}

	t.sess.emit(agent.AgentEvent{
		Type:     agent.EventToolCall,
		ToolID:   call.ID,
		ToolName: call.Name,
		ToolArgs: args,
	})

	out := t.sess.executor.Execute(ctx, agent.ToolInvocation{
		ID:   call.ID,
		Name: call.Name,
		Args: args,
	})

	t.sess.emit(agent.AgentEvent{
		Type:     agent.EventToolDone,
		ToolID:   call.ID,
		ToolName: call.Name,
		ToolOK:   out.OK,
	})

	if !out.OK {
		// The browser's own message, verbatim: it is the one that names the id
		// that did not exist, which is what the model needs to correct itself.
		fail := map[string]any{"ok": false, "error": out.Error}
		// A rejected reference is nearly always a guessed id for a shape this
		// turn just created, so name the ids that do exist. Without this the
		// model's recovery was to create the shape a second time (measured).
		if ids := t.created(); len(ids) > 0 {
			fail["shapes_you_created_this_turn"] = ids
			fail["hint"] = "use one of these existing ids; do not create the shape again"
		}
		return reply(fail)
	}

	res := map[string]any{"ok": true}
	switch len(out.ShapeIDs) {
	case 0:
	case 1:
		// Stating the id explicitly is load-bearing: the model cannot know a new
		// shape's id (the browser assigns it) and will otherwise invent one for
		// the following create_arrow, which the canvas then rejects.
		//
		// The "already exists" half is equally load-bearing and was measured: told
		// only "use this id", the model recovered from a rejected arrow by calling
		// create_shape AGAIN, duplicating the box. It needs to be told the shape is
		// done, not just what to call it.
		t.madeIDs = append(t.madeIDs, out.ShapeIDs[0])
		res["id"] = out.ShapeIDs[0]
		res["note"] = "this shape now exists — do not create it again; use this exact id to reference it"
	default:
		t.madeIDs = append(t.madeIDs, out.ShapeIDs...)
		res["ids"] = out.ShapeIDs
		res["note"] = "these shapes now exist — do not create them again; use these exact ids"
	}
	if warn != "" {
		res["warn"] = warn
	}
	return reply(res)
}

// coordinateKeys are the arguments no tool accepts. The model never places
// shapes in pixels (planv2 §2.2); the frontend ignores unknown keys, so leaving
// them in would let the model believe it positioned something when it did not.
var coordinateKeys = []string{"x", "y", "w", "h", "width", "height"}

// sanitizeArgs strips arguments outside the tool schema and reports the ones
// worth telling the model about.
func sanitizeArgs(raw json.RawMessage) (json.RawMessage, string) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw, ""
	}
	var found []string
	for _, k := range coordinateKeys {
		if _, ok := obj[k]; ok {
			found = append(found, k)
			delete(obj, k)
		}
	}
	if len(found) == 0 {
		return raw, ""
	}
	cleaned, err := json.Marshal(obj)
	if err != nil {
		return raw, ""
	}
	return cleaned, strings.Join(found, "/") + " ignored — position with near and direction, not coordinates"
}
