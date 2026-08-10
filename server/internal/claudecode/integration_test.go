package claudecode

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anubhavitis/whiteboard/server/internal/agent"
)

// recordingExecutor stands in for the browser: it accepts every tool call and
// hands back plausible shape ids.
type recordingExecutor struct {
	mu    sync.Mutex
	calls []agent.ToolInvocation
	n     int
}

func (r *recordingExecutor) Execute(_ context.Context, call agent.ToolInvocation) agent.ToolOutcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
	r.n++
	return agent.ToolOutcome{OK: true, ShapeIDs: []string{"shape:test" + string(rune('0'+r.n))}}
}

func (r *recordingExecutor) snapshot() []agent.ToolInvocation {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]agent.ToolInvocation, len(r.calls))
	copy(out, r.calls)
	return out
}

func requireClaude(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_AGENT_TEST") == "" {
		t.Skip("set RUN_AGENT_TEST=1 (spawns claude, costs subscription usage)")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not on PATH")
	}
}

// The whole stack, minus the browser: a real Claude Code subprocess, our MCP
// server, per-session routing, and a tool call that round-trips.
func TestClaudeCodeDrawsViaMCP(t *testing.T) {
	requireClaude(t)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mcp := NewMCPServer(log)
	srv := httptest.NewServer(mcp)
	defer srv.Close()

	exec := &recordingExecutor{}
	factory := NewFactory(mcp, srv.URL, agent.SystemPrompt, log)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	session, err := factory.New(ctx, "testsession", exec)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	defer session.Close()

	canvas := json.RawMessage(`{"shapes":[{"id":"shape:api","type":"geo","text":"API Gateway","x":0,"y":0,"w":160,"h":90}],"arrows":[],"truncated":false,"totalShapes":1}`)
	if err := session.SendTurn(ctx, "Add a Redis cache below the API Gateway and connect them.", canvas); err != nil {
		t.Fatalf("send turn: %v", err)
	}

	var text strings.Builder
	var errs []string
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out; text so far: %q", text.String())
		case ev, ok := <-session.Events():
			if !ok {
				t.Fatal("event channel closed before turn_done")
			}
			switch ev.Type {
			case agent.EventTextDelta:
				text.WriteString(ev.Text)
			case agent.EventError:
				errs = append(errs, ev.Text)
			case agent.EventTurnDone:
				calls := exec.snapshot()
				t.Logf("assistant: %s", strings.TrimSpace(text.String()))
				for _, c := range calls {
					t.Logf("tool call: %s %s", c.Name, c.Args)
				}
				if len(errs) > 0 {
					t.Logf("errors: %v", errs)
				}
				if len(calls) == 0 {
					t.Fatal("agent produced no tool calls — it did not draw")
				}
				// The point of the whole exercise: a shape was created.
				var sawCreate bool
				for _, c := range calls {
					if c.Name == "create_shape" {
						sawCreate = true
						// And it must NOT carry coordinates.
						var args map[string]any
						json.Unmarshal(c.Args, &args)
						for _, banned := range []string{"x", "y", "position"} {
							if _, bad := args[banned]; bad {
								t.Errorf("create_shape carried a coordinate arg %q: %s", banned, c.Args)
							}
						}
					}
				}
				if !sawCreate {
					t.Error("no create_shape call")
				}
				return
			}
		}
	}
}

// The Claude Code session id must surface, since --resume (planv2 §5.1) and
// persistence (§3.3) both depend on it.
func TestSessionIDIsCaptured(t *testing.T) {
	requireClaude(t)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mcp := NewMCPServer(log)
	srv := httptest.NewServer(mcp)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	sess, err := New(ctx, Config{
		SessionID:    "idcheck",
		MCPBaseURL:   srv.URL,
		SystemPrompt: "Reply with one word.",
		Log:          log,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sess.Close()

	if err := sess.SendTurn(ctx, "Say hello.", nil); err != nil {
		t.Fatalf("send: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for turn_done")
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("events closed early")
			}
			if ev.Type == agent.EventTurnDone {
				if id := sess.ClaudeSessionID(); id == "" {
					t.Error("no claude session id captured — --resume impossible")
				} else {
					t.Logf("claude session id: %s", id)
				}
				return
			}
		}
	}
}
