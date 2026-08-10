package ws

import (
	"context"
	"sync"
	"time"

	"github.com/anubhavitis/whiteboard/server/internal/agent"
)

// browserExecutor implements agent.ToolExecutor by round-tripping to the
// browser, which owns the tldraw store (D5).
//
// It must be safe for concurrent use: Claude Code can request several tools at
// once, and each call blocks its own goroutine until the browser answers.
type browserExecutor struct {
	send func(ctx context.Context, call ToolCall) error
	// timeout is a field so tests need not wait the production duration.
	timeout time.Duration

	mu      sync.Mutex
	waiters map[string]chan ToolResult
}

func newBrowserExecutor(send func(ctx context.Context, call ToolCall) error) *browserExecutor {
	return &browserExecutor{send: send, timeout: toolTimeout, waiters: make(map[string]chan ToolResult)}
}

// toolTimeout bounds a single tool round-trip. A browser that never answers
// (tab closed mid-turn, JS exception) would otherwise wedge the agent until the
// whole session dies.
const toolTimeout = 30 * time.Second

func (e *browserExecutor) Execute(ctx context.Context, call agent.ToolInvocation) agent.ToolOutcome {
	ch := make(chan ToolResult, 1)

	e.mu.Lock()
	e.waiters[call.ID] = ch
	e.mu.Unlock()

	defer func() {
		e.mu.Lock()
		delete(e.waiters, call.ID)
		e.mu.Unlock()
	}()

	if err := e.send(ctx, ToolCall{ID: call.ID, Name: call.Name, Args: call.Args}); err != nil {
		return agent.ToolOutcome{OK: false, Error: "could not reach the canvas: " + err.Error()}
	}

	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	select {
	case <-ctx.Done():
		return agent.ToolOutcome{OK: false, Error: "the canvas did not respond in time"}
	case res := <-ch:
		if !res.OK {
			msg := res.Error
			if msg == "" {
				msg = "the canvas rejected this change"
			}
			return agent.ToolOutcome{OK: false, Error: msg}
		}
		return agent.ToolOutcome{OK: true, ShapeIDs: res.ResultingShapes}
	}
}

// deliver hands a browser result to whichever call is waiting for it. A result
// for an unknown id is dropped: its turn has already ended.
func (e *browserExecutor) deliver(res ToolResult) {
	e.mu.Lock()
	ch, ok := e.waiters[res.ID]
	e.mu.Unlock()
	if ok {
		ch <- res
	}
}
