package native

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/anubhavitis/whiteboard/server/internal/agent"
)

// client is one HTTP round trip to an OpenAI-compatible endpoint. It knows SSE
// and nothing about loops: given a conversation it returns the assembled text
// and any tool calls the model asked for.
type client struct {
	http    *http.Client
	baseURL string
	model   string
	log     *slog.Logger

	// seq makes synthesised tool-call ids unique for the life of the session,
	// not just within one response: ws/executor keys its waiters by id, so a
	// collision across turns would deliver a result to the wrong caller.
	seqMu sync.Mutex
	seq   int
}

func (c *client) nextSeq() string {
	c.seqMu.Lock()
	defer c.seqMu.Unlock()
	c.seq++
	return strconv.Itoa(c.seq)
}

// message is one entry in the conversation sent upstream.
//
// Content is not omitempty: an assistant turn that is only tool calls has empty
// content, and mlx_lm coerces null to "" but expects the key to exist.
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`

	// ToolCalls is set on an assistant message that requested tools. Replaying
	// it is how the model sees what it already asked for.
	ToolCalls []toolCallWire `json:"tool_calls,omitempty"`

	// ToolCallID ties a role:"tool" result back to the call it answers.
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
}

// toolCallWire is the OpenAI on-the-wire shape. Arguments stays a JSON *string*
// in both directions: that is what mlx_lm emits, and its process_message_content
// re-parses the string when templating, so sending an object breaks it.
type toolCallWire struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// toolCall is a parsed request to run one canvas tool.
type toolCall struct {
	ID   string
	Name string
	// Args is the raw arguments JSON, still unparsed: the executor wants raw
	// json.RawMessage and the browser is the authority on what is valid.
	Args json.RawMessage
	// ArgsRaw is the original string form, replayed verbatim in history so the
	// model sees exactly what it sent.
	ArgsRaw string
	// Malformed marks arguments that were not valid JSON. The loop turns these
	// into a tool error rather than sending them to the browser.
	Malformed bool
}

// completion is one model response.
type completion struct {
	Text  string
	Calls []toolCall
	// FinishReason is "tool_calls" when the model asked for tools. It is
	// cross-checked against Calls: mlx_lm silently drops a truncated tool call
	// (server.py:82-87), which otherwise looks like a turn that just ended.
	FinishReason string
	SawReasoning bool
}

type chatRequest struct {
	Model    string           `json:"model"`
	Messages []message        `json:"messages"`
	Stream   bool             `json:"stream"`
	Tools    []map[string]any `json:"tools,omitempty"`
}

// inlineToolCall matches the <tool_call>{...}</tool_call> markup some chat
// templates emit as plain text.
//
// This path is dormant against mlx_lm 0.31.3 + Qwen3: that pairing resolves to
// the json_tools parser and always reports structured tool_calls (measured in
// spike S3 — 10/10 trials came back via tool_calls, 0 inline). It exists because
// when a model's template is NOT recognised, mlx_lm warns and drops tools
// silently (server.py:537), and the markup can then surface as text. Firing is
// therefore a signal that the primary path broke, and it logs at WARN.
var inlineToolCall = regexp.MustCompile(`(?s)<tool_call>\s*(\{.*?\})\s*</tool_call>`)

// complete streams one model response, emitting text deltas through onDelta as
// they arrive so the UI stays live during a long generation.
func (c *client) complete(
	ctx context.Context,
	convo []message,
	tools []map[string]any,
	onDelta func(string),
) (completion, error) {
	body, err := json.Marshal(chatRequest{
		Model: c.model, Messages: convo, Stream: true, Tools: tools,
	})
	if err != nil {
		return completion{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(c.baseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return completion{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return completion{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return completion{}, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}

	return c.readStream(resp.Body, onDelta)
}

// streamFrame is the subset of an SSE chunk we read.
type streamFrame struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			// Thinking models stream chain-of-thought here, not in content.
			Reasoning string `json:"reasoning"`
			ToolCalls []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

func (c *client) readStream(r io.Reader, onDelta func(string)) (completion, error) {
	var out completion
	var text strings.Builder

	// Accumulate by index so a server that fragments arguments across frames
	// still assembles. mlx_lm 0.31.3 never does — it emits each call whole in one
	// frame — but real OpenAI does, and this is cheap insurance for the day the
	// endpoint changes underneath us.
	type acc struct {
		id, name string
		args     strings.Builder
	}
	byIndex := map[int]*acc{}
	var order []int
	// suppressed goes true the moment inline tool-call markup appears, so the
	// remainder is buffered for the fallback parser instead of streamed.
	suppressed := false

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var frame streamFrame
		if err := json.Unmarshal([]byte(data), &frame); err != nil {
			// One malformed frame should not kill a turn that is otherwise fine.
			c.log.Warn("native: unparseable stream chunk", "err", err)
			continue
		}

		for _, ch := range frame.Choices {
			if ch.FinishReason != "" {
				out.FinishReason = ch.FinishReason
			}
			if ch.Delta.Reasoning != "" {
				out.SawReasoning = true
			}
			if ch.Delta.Content != "" {
				text.WriteString(ch.Delta.Content)
				// Once markup appears, stop streaming: the rest of this response is
				// a tool call in prose clothing, and it must not reach the
				// transcript. Deltas already sent cannot be recalled, which is why
				// the check is on the whole buffer rather than this chunk.
				if suppressed || strings.Contains(text.String(), "<tool_call>") {
					suppressed = true
				} else if onDelta != nil {
					onDelta(ch.Delta.Content)
				}
			}
			for i, tc := range ch.Delta.ToolCalls {
				idx := i
				if tc.Index != nil {
					idx = *tc.Index
				}
				a, seen := byIndex[idx]
				if !seen {
					a = &acc{}
					byIndex[idx] = a
					order = append(order, idx)
				}
				// A frame carrying a name starts (or names) the call; a frame with
				// only arguments continues one.
				if tc.Function.Name != "" {
					a.name = tc.Function.Name
				}
				if tc.ID != "" {
					a.id = tc.ID
				}
				a.args.WriteString(tc.Function.Arguments)
			}
		}
	}
	if err := sc.Err(); err != nil {
		out.Text = text.String()
		return out, err
	}

	out.Text = text.String()
	for _, idx := range order {
		a := byIndex[idx]
		if a.name == "" {
			c.log.Warn("native: tool call frame with no name", "index", idx)
			continue
		}
		// An id is mandatory downstream: ws/executor keys its waiting channels by
		// it, so two calls sharing an id would cross their results. mlx_lm always
		// supplies a UUID (server.py:62), but a different endpoint might not.
		id := a.id
		if id == "" {
			id = fmt.Sprintf("native-%s-%d", c.nextSeq(), idx)
		}
		out.Calls = append(out.Calls, newToolCall(id, a.name, a.args.String()))
	}

	// Dormant fallback: only consult the text when the structured path produced
	// nothing at all. Deliberately not gated on finish_reason — a template that
	// emits markup as prose typically finishes with "stop".
	if len(out.Calls) == 0 {
		if calls, stripped, found := parseInlineToolCalls(out.Text); found {
			c.log.Warn("native: tool calls arrived as inline markup, not tool_calls — " +
				"the endpoint's tool parser may not recognise this model")
			out.Calls = calls
			out.Text = stripped
		}
	}
	return out, nil
}

func newToolCall(id, name, args string) toolCall {
	tc := toolCall{ID: id, Name: name, ArgsRaw: args}
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		trimmed = "{}"
		tc.ArgsRaw = "{}"
	}
	if !json.Valid([]byte(trimmed)) {
		tc.Malformed = true
		return tc
	}
	tc.Args = json.RawMessage(trimmed)
	return tc
}

// parseInlineToolCalls extracts <tool_call>{...}</tool_call> blocks and returns
// the text with those blocks removed, so markup never reaches the transcript.
func parseInlineToolCalls(text string) ([]toolCall, string, bool) {
	matches := inlineToolCall.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil, text, false
	}
	var calls []toolCall
	for _, m := range matches {
		var obj struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(m[1]), &obj); err != nil || obj.Name == "" {
			continue
		}
		args := string(obj.Arguments)
		if args == "" || args == "null" {
			args = "{}"
		}
		calls = append(calls, newToolCall("", obj.Name, args))
	}
	if len(calls) == 0 {
		return nil, text, false
	}
	return calls, strings.TrimSpace(inlineToolCall.ReplaceAllString(text, "")), true
}

// wire renders parsed calls back into the assistant message that replays them.
func wire(calls []toolCall) []toolCallWire {
	out := make([]toolCallWire, 0, len(calls))
	for _, c := range calls {
		var w toolCallWire
		w.ID = c.ID
		w.Type = "function"
		w.Function.Name = c.Name
		w.Function.Arguments = c.ArgsRaw
		out = append(out, w)
	}
	return out
}

// toolNames is the set the model is allowed to call, checked in Go before a
// round trip to the browser.
func toolNames() map[string]bool {
	names := map[string]bool{}
	for _, t := range agent.Tools() {
		names[t.Name] = true
	}
	return names
}
