// Package native is the second AgentSession implementation: a loop we own,
// talking to an OpenAI-compatible endpoint (planv2.md §1.3).
//
// Unlike the Claude Code agent, nothing here spawns a subprocess and no tool
// call arrives from outside: the loop lives in this process and tool calls are
// parsed *out of* the streamed response (turn.go).
//
// THE IMPORT RULE (see package agent): this package and internal/claudecode are
// the only places allowed to know about a model transport. Keep HTTP here.
//
// Tools are offered only when the session has a ToolExecutor. Without one it
// degrades to chat, which is what the tests that pass nil rely on.
package native

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/anubhavitis/whiteboard/server/internal/agent"
)

// Session is one conversation with a local model. It keeps its own history:
// an OpenAI-compatible endpoint is stateless, so multi-turn memory is our job
// (this is the main structural difference from the Claude Code session, which
// gets memory from the subprocess and --resume).
type Session struct {
	client *client
	prompt string
	log    *slog.Logger
	// executor is how a tool call reaches the canvas. Nil means chat-only.
	executor agent.ToolExecutor
	events   chan agent.AgentEvent
	closed   chan struct{}
	once     sync.Once

	// mu guards history and cancelTurn: SendTurn returns before its turn
	// finishes, so a second turn can arrive while the first is still streaming.
	mu      sync.Mutex
	history []message

	// cancelTurn stops the turn in flight. A new turn cancels the previous one:
	// two loops interleaving tool calls would corrupt both the canvas and the
	// ordering of this session's history.
	cancelTurn context.CancelFunc
}

// SendTurn queues one turn and returns; progress arrives on Events.
func (s *Session) SendTurn(ctx context.Context, text string, canvas json.RawMessage) error {
	user, err := buildUserContent(text, canvas)
	if err != nil {
		return err
	}

	s.mu.Lock()
	// One turn at a time: a second turn supersedes the first rather than running
	// beside it.
	if s.cancelTurn != nil {
		s.cancelTurn()
	}
	turnCtx, cancel := context.WithCancel(ctx)
	s.cancelTurn = cancel

	s.history = append(s.history, message{Role: "user", Content: user})
	// Copy under the lock: the request must not observe a later turn's append.
	convo := make([]message, 0, len(s.history)+1)
	convo = append(convo, message{Role: "system", Content: s.prompt})
	convo = append(convo, s.history...)
	s.mu.Unlock()

	go s.runTurn(turnCtx, convo)
	return nil
}

// Cancel stops the turn in flight. Safe to call with no turn running.
func (s *Session) Cancel() {
	s.mu.Lock()
	cancel := s.cancelTurn
	s.cancelTurn = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
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
	newTurn(s, convo).run(ctx)
}

// tools returns the canvas tools to offer the model, or nil for a chat-only
// session. Without an executor there is nothing to run a tool call against, so
// advertising tools would invite calls we would have to reject.
func (s *Session) tools() []map[string]any {
	if s.executor == nil {
		return nil
	}
	return agent.OpenAITools()
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
//
// It deliberately does NOT close the events channel: a turn goroutine may still
// be writing to it, and closing would panic. emit selects on s.closed instead.
func (s *Session) Close() error {
	s.Cancel()
	s.once.Do(func() { close(s.closed) })
	return nil
}
