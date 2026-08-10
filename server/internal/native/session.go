// Package native is the second AgentSession implementation: a loop we own,
// talking to an OpenAI-compatible endpoint (planv2.md §1.3).
//
// Unlike the Claude Code agent, nothing here spawns a subprocess and no tool
// call arrives from outside — the loop lives in this process, and when tool
// support lands it will parse calls *out of* the streamed response.
//
// THE IMPORT RULE (see package agent): this package and internal/claudecode are
// the only places allowed to know about a model transport. Keep HTTP here.
//
// Scope today is chat-only, deliberately. planv2.md §0.7 gates whether the
// local model gets canvas tools on spike S3 scoring >=8/10; until that verdict
// exists, a tool loop here would be speculative.
package native

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/anubhavitis/whiteboard/server/internal/agent"
)

// Session is one conversation with a local model. It keeps its own history:
// an OpenAI-compatible endpoint is stateless, so multi-turn memory is our job
// (this is the main structural difference from the Claude Code session, which
// gets memory from the subprocess and --resume).
type Session struct {
	client  *http.Client
	baseURL string
	model   string
	prompt  string
	log     *slog.Logger
	events  chan agent.AgentEvent
	closed  chan struct{}
	once    sync.Once

	// mu guards history: SendTurn returns before its turn finishes, so a
	// second turn can arrive while the first is still streaming.
	mu      sync.Mutex
	history []message
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// SendTurn queues one turn and returns; progress arrives on Events.
func (s *Session) SendTurn(ctx context.Context, text string, canvas json.RawMessage) error {
	user, err := buildUserContent(text, canvas)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.history = append(s.history, message{Role: "user", Content: user})
	// Copy under the lock: the request must not observe a later turn's append.
	convo := make([]message, 0, len(s.history)+1)
	convo = append(convo, message{Role: "system", Content: s.prompt})
	convo = append(convo, s.history...)
	s.mu.Unlock()

	go s.runTurn(ctx, convo)
	return nil
}

// buildUserContent packs the message and the canvas into one user turn. The
// canvas is passed through verbatim so the serializer stays the single authority
// on its shape.
func buildUserContent(text string, canvas json.RawMessage) (string, error) {
	if len(canvas) == 0 {
		canvas = json.RawMessage("null")
	}
	if !json.Valid(canvas) {
		return "", fmt.Errorf("canvas is not valid JSON")
	}
	var b strings.Builder
	b.WriteString("<canvas>\n")
	b.Write(canvas)
	b.WriteString("\n</canvas>\n\n")
	b.WriteString(text)
	return b.String(), nil
}

func (s *Session) runTurn(ctx context.Context, convo []message) {
	reply, err := s.stream(ctx, convo)
	if err != nil {
		// A cancelled turn is the Stop button, not a failure to report.
		if errors.Is(err, context.Canceled) {
			s.emit(agent.AgentEvent{Type: agent.EventTurnDone})
			return
		}
		s.log.Error("native turn failed", "err", err)
		s.emit(agent.AgentEvent{
			Type: agent.EventError,
			Text: "the local model could not be reached: " + err.Error(),
		})
		s.emit(agent.AgentEvent{Type: agent.EventTurnDone})
		return
	}

	if reply != "" {
		s.mu.Lock()
		s.history = append(s.history, message{Role: "assistant", Content: reply})
		s.mu.Unlock()
	}
	s.emit(agent.AgentEvent{Type: agent.EventTurnDone})
}

// chatRequest is the subset of the OpenAI chat API we send. Tools are absent on
// purpose — see the package comment.
type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// stream posts the turn and emits text deltas as they arrive, returning the
// assembled reply for the history.
func (s *Session) stream(ctx context.Context, convo []message) (string, error) {
	body, err := json.Marshal(chatRequest{Model: s.model, Messages: convo, Stream: true})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(s.baseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}

	var full strings.Builder
	// sawReasoning distinguishes "the model said nothing" from "the model spent
	// its whole budget thinking", which are different problems for the user.
	sawReasoning := false
	sc := bufio.NewScanner(resp.Body)
	// SSE lines can exceed bufio's default 64k on long single-chunk replies.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
					// Qwen3 and other thinking models stream their chain of
					// thought here, NOT in content — verified against
					// mlx_lm.server 0.31.3, which emits {"delta":{"reasoning":
					// "..."}} frames before any content arrives. Reading only
					// content leaves the UI frozen for the whole thinking phase.
					Reasoning string `json:"reasoning"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// One malformed frame should not kill a turn that is otherwise fine.
			s.log.Warn("native: unparseable stream chunk", "err", err)
			continue
		}
		for _, c := range chunk.Choices {
			// Thinking is not shown as assistant text and never enters history:
			// it is the model's scratchpad, and replaying it next turn would
			// both confuse the model and burn context.
			if c.Delta.Reasoning != "" {
				sawReasoning = true
			}
			if c.Delta.Content == "" {
				continue
			}
			full.WriteString(c.Delta.Content)
			s.emit(agent.AgentEvent{Type: agent.EventTextDelta, Text: c.Delta.Content})
		}
	}
	if err := sc.Err(); err != nil {
		// Return what we have: a truncated reply already reached the UI.
		return full.String(), err
	}
	if full.Len() == 0 && sawReasoning {
		// The model thought until it ran out of budget and never spoke. Saying
		// so beats an empty bubble the user cannot interpret.
		s.emit(agent.AgentEvent{
			Type: agent.EventError,
			Text: "the local model spent its whole response thinking and never answered — try a shorter question, or a model with thinking disabled",
		})
	}
	return full.String(), nil
}

func (s *Session) emit(ev agent.AgentEvent) {
	select {
	case s.events <- ev:
	case <-s.closed:
	}
}

// Events returns the session's event stream.
func (s *Session) Events() <-chan agent.AgentEvent { return s.events }

// Close is safe to call twice.
func (s *Session) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}
