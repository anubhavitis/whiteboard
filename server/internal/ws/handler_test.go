package ws

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/anubhavitis/whiteboard/server/internal/agent"
	"github.com/coder/websocket"
)

// toolFactory produces a session that calls one canvas tool per turn, which is
// the shape of every real drawing turn.
type toolFactory struct {
	mu       sync.Mutex
	sessions []*toolSession
}

func (f *toolFactory) Name() string { return "tool-test" }

func (f *toolFactory) New(_ context.Context, id string, exec agent.ToolExecutor) (agent.AgentSession, error) {
	s := &toolSession{
		exec:   exec,
		events: make(chan agent.AgentEvent, 16),
		closed: make(chan struct{}),
	}
	f.mu.Lock()
	f.sessions = append(f.sessions, s)
	f.mu.Unlock()
	return s, nil
}

type toolSession struct {
	exec     agent.ToolExecutor
	events   chan agent.AgentEvent
	closed   chan struct{}
	once     sync.Once
	outcomes chan agent.ToolOutcome
}

func (s *toolSession) SendTurn(ctx context.Context, _ string, _ json.RawMessage) error {
	go func() {
		// Blocks until the browser answers — the exact pattern that deadlocked
		// when turns ran inside the socket read loop.
		out := s.exec.Execute(ctx, agent.ToolInvocation{
			ID:   "toolu_1",
			Name: "create_shape",
			Args: json.RawMessage(`{"shape":"box","text":"Cache"}`),
		})
		if s.outcomes != nil {
			s.outcomes <- out
		}
		if out.OK && len(out.ShapeIDs) > 0 {
			s.emit(agent.AgentEvent{Type: agent.EventTextDelta, Text: "got:" + out.ShapeIDs[0]})
		} else {
			s.emit(agent.AgentEvent{Type: agent.EventTextDelta, Text: "failed:" + out.Error})
		}
		s.emit(agent.AgentEvent{Type: agent.EventTurnDone})
	}()
	return nil
}

func (s *toolSession) emit(ev agent.AgentEvent) {
	select {
	case s.events <- ev:
	case <-s.closed:
	}
}

func (s *toolSession) Events() <-chan agent.AgentEvent { return s.events }

func (s *toolSession) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

// Regression test for a deadlock: dispatching a turn synchronously inside the
// read loop meant the tool_result the turn was waiting for could never be read,
// because the only reader was blocked on the turn. Every drawing turn hung.
func TestToolCallRoundTripDoesNotDeadlock(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(NewHandler(&toolFactory{}, log, []string{"*"}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[4:]+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	write(t, ctx, conn, Envelope{Type: TypeUserMessage, Payload: json.RawMessage(`{"text":"draw"}`)})

	var sawToolCall, sawTurnEnd bool
	var text string
	for !sawTurnEnd {
		env := read(t, ctx, conn)
		switch env.Type {
		case TypeToolCall:
			sawToolCall = true
			var call ToolCall
			if err := json.Unmarshal(env.Payload, &call); err != nil {
				t.Fatalf("tool_call payload: %v", err)
			}
			if call.Name != "create_shape" {
				t.Errorf("tool name = %q, want create_shape", call.Name)
			}
			// Reply exactly as the frontend executor would.
			write(t, ctx, conn, Envelope{
				Type:    TypeToolResult,
				Payload: json.RawMessage(`{"id":"` + call.ID + `","ok":true,"resulting_shape_ids":["shape:new"]}`),
			})
		case TypeAssistantDelta:
			var d AssistantDelta
			json.Unmarshal(env.Payload, &d)
			text += d.Text
		case TypeTurnEnd:
			sawTurnEnd = true
		case TypeError:
			t.Fatalf("unexpected error frame: %s", env.Payload)
		}
	}

	if !sawToolCall {
		t.Error("never received a tool_call")
	}
	if text != "got:shape:new" {
		t.Errorf("assistant text = %q, want the tool's shape id echoed back", text)
	}
}

// A tool failure must reach the agent as a failed outcome — so the model can
// adapt — rather than being swallowed or killing the turn.
func TestToolFailureReachesTheAgent(t *testing.T) {
	factory := &toolFactory{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(NewHandler(factory, log, []string{"*"}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[4:]+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	write(t, ctx, conn, Envelope{Type: TypeUserMessage, Payload: json.RawMessage(`{"text":"draw"}`)})

	var text string
	for {
		env := read(t, ctx, conn)
		if env.Type == TypeToolCall {
			var call ToolCall
			json.Unmarshal(env.Payload, &call)
			write(t, ctx, conn, Envelope{
				Type:    TypeToolResult,
				Payload: json.RawMessage(`{"id":"` + call.ID + `","ok":false,"error":"no shape with id shape:x"}`),
			})
			continue
		}
		if env.Type == TypeAssistantDelta {
			var d AssistantDelta
			json.Unmarshal(env.Payload, &d)
			text += d.Text
		}
		if env.Type == TypeTurnEnd {
			break
		}
	}

	if text != "failed:no shape with id shape:x" {
		t.Errorf("agent saw %q, want the browser's error message", text)
	}
}

// An unanswered tool call must time out rather than wedge the agent forever
// (tab closed mid-turn, JS exception).
func TestUnansweredToolCallTimesOut(t *testing.T) {
	exec := newBrowserExecutor(func(context.Context, ToolCall) error { return nil })
	exec.timeout = 200 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan agent.ToolOutcome, 1)
	go func() {
		done <- exec.Execute(ctx, agent.ToolInvocation{ID: "toolu_never", Name: "create_shape"})
	}()

	select {
	case out := <-done:
		if out.OK {
			t.Error("expected failure when the browser never answers")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return — the agent would hang forever")
	}
}

func write(t *testing.T, ctx context.Context, conn *websocket.Conn, env Envelope) {
	t.Helper()
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func read(t *testing.T, ctx context.Context, conn *websocket.Conn) Envelope {
	t.Helper()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return env
}
