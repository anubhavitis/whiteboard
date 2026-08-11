package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
)

// EchoFactory produces sessions that stream the user's own text back.
//
// It exists so the canvas is usable when no agent is available (no `claude` on
// PATH, no local model yet) and as a fixture for transport tests that should
// not spend subscription usage.
type EchoFactory struct{}

func (EchoFactory) Name() string { return "echo" }

func (EchoFactory) New(_ context.Context, _ string, _ ToolExecutor) (AgentSession, error) {
	return &echoSession{events: make(chan AgentEvent, 16), closed: make(chan struct{})}, nil
}

type echoSession struct {
	events chan AgentEvent
	closed chan struct{}
	once   sync.Once
}

func (e *echoSession) SendTurn(_ context.Context, text string, _ json.RawMessage) error {
	go func() {
		e.emit(AgentEvent{Type: EventTextDelta, Text: "echo: "})
		for i, word := range strings.Fields(text) {
			if i > 0 {
				word = " " + word
			}
			e.emit(AgentEvent{Type: EventTextDelta, Text: word})
		}
		e.emit(AgentEvent{Type: EventTurnDone})
	}()
	return nil
}

func (e *echoSession) emit(ev AgentEvent) {
	select {
	case e.events <- ev:
	case <-e.closed:
	}
}

func (e *echoSession) Events() <-chan AgentEvent { return e.events }

// Cancel is a no-op: an echo turn is a handful of sends and is already over.
func (e *echoSession) Cancel() {}

func (e *echoSession) Close() error {
	e.once.Do(func() {
		close(e.closed)
		close(e.events)
	})
	return nil
}
