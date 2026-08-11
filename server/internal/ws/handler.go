package ws

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/anubhavitis/whiteboard/server/internal/agent"
	"github.com/coder/websocket"
)

// Handler upgrades HTTP requests to WebSocket sessions and connects each one to
// an agent.
//
// It knows nothing about Claude Code, MCP, or any model API — only the
// agent.Factory it was given. That is the import rule from planv2 in practice.
type Handler struct {
	// factories are the agents the UI may switch between, in dropdown order.
	// The first is the default for a new session.
	factories []agent.Factory
	log       *slog.Logger
	origins   []string
}

func NewHandler(factory agent.Factory, log *slog.Logger, allowedOrigins []string) *Handler {
	return &Handler{factories: []agent.Factory{factory}, log: log, origins: allowedOrigins}
}

// NewHandlerWithAgents builds a handler the browser can switch agents on
// (planv2.md §1.2). The first factory is the default.
func NewHandlerWithAgents(factories []agent.Factory, log *slog.Logger, allowedOrigins []string) *Handler {
	return &Handler{factories: factories, log: log, origins: allowedOrigins}
}

func (h *Handler) factoryNamed(name string) agent.Factory {
	for _, f := range h.factories {
		if f.Name() == name {
			return f
		}
	}
	return nil
}

func (h *Handler) agentNames() []string {
	names := make([]string, 0, len(h.factories))
	for _, f := range h.factories {
		names = append(names, f.Name())
	}
	return names
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: h.origins,
	})
	if err != nil {
		h.log.Error("websocket accept failed", "err", err)
		return
	}
	defer conn.CloseNow()

	// Canvas context and model responses both run large; the default 32KiB read
	// limit is not enough once a canvas has more than a handful of shapes.
	conn.SetReadLimit(8 << 20)

	sessionID := newSessionID()
	s := &session{conn: conn, log: h.log, id: sessionID}
	s.executor = newBrowserExecutor(s.SendToolCall)

	h.log.Info("session opened", "session", sessionID, "remote", r.RemoteAddr)
	defer h.log.Info("session closed", "session", sessionID)

	// The agent subprocess starts with the socket and dies with it.
	current := h.factories[0]
	agentSession, err := current.New(r.Context(), sessionID, s.executor)
	if err != nil {
		h.log.Error("agent start failed", "err", err, "session", sessionID)
		s.SendError(r.Context(), "could not start the agent: "+err.Error())
		conn.Close(websocket.StatusInternalError, "agent unavailable")
		return
	}
	s.setAgent(agentSession, current.Name())
	defer s.closeAgent()

	// Agent events flow to the browser on their own goroutine, so the read loop
	// stays free to receive tool results — the deadlock this design had before.
	go h.forwardEvents(r.Context(), s, agentSession)

	// Tell the UI what it may switch to, so the dropdown is built from what the
	// server actually has rather than a list that can drift.
	if err := s.sendTyped(r.Context(), TypeAgentsAvailable, AgentsAvailable{
		Names:   h.agentNames(),
		Current: current.Name(),
	}); err != nil {
		h.log.Warn("could not send agent list", "err", err, "session", sessionID)
	}

	if err := h.pump(r.Context(), s); err != nil {
		status := websocket.CloseStatus(err)
		switch {
		// StatusNoStatusRcvd is what a browser tab closing usually produces:
		// a close frame with no code. It is not a failure.
		case status == websocket.StatusNormalClosure,
			status == websocket.StatusGoingAway,
			status == websocket.StatusNoStatusRcvd:
		case errors.Is(err, context.Canceled):
		default:
			h.log.Error("session ended", "err", err, "session", sessionID)
		}
		return
	}
	conn.Close(websocket.StatusNormalClosure, "")
}

// forwardEvents translates agent events into browser frames.
//
// It exits when the agent it was started for is no longer the session's current
// one. Neither agent implementation closes its event channel on Close — a turn
// goroutine may still be writing to it, and closing would panic — so ranging
// over Events() alone would leak this goroutine across a switch_agent, leaving
// two forwarders writing to one socket.
func (h *Handler) forwardEvents(ctx context.Context, s *session, as agent.AgentSession) {
	events := as.Events()
	for {
		var ev agent.AgentEvent
		select {
		case <-ctx.Done():
			return
		case e, ok := <-events:
			if !ok {
				return
			}
			ev = e
		}

		// A switch replaced this agent; its remaining events are not the
		// session's conversation any more.
		if live, _ := s.takeAgent(); live != as {
			return
		}

		var err error
		switch ev.Type {
		case agent.EventTextDelta:
			err = s.SendDelta(ctx, ev.Text)
		case agent.EventTurnDone:
			err = s.SendTurnEnd(ctx)
		case agent.EventError:
			err = s.SendError(ctx, ev.Text)
		case agent.EventToolCall:
			// The browser learns about tool calls through the executor, which
			// is what actually needs a result. Log only.
			h.log.Info("agent tool call", "session", s.id, "tool", ev.ToolName)
		}
		if err != nil {
			h.log.Error("forward failed", "err", err, "session", s.id)
			return
		}
	}
}

func (h *Handler) pump(ctx context.Context, s *session) error {
	for {
		_, data, err := s.conn.Read(ctx)
		if err != nil {
			return err
		}

		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			// A malformed frame is the client's bug, not a reason to drop the
			// session. Report it and keep going.
			if err := s.SendError(ctx, "malformed message envelope"); err != nil {
				return err
			}
			continue
		}

		if err := h.dispatch(ctx, s, env); err != nil {
			return err
		}
	}
}

func (h *Handler) dispatch(ctx context.Context, s *session, env Envelope) error {
	switch env.Type {
	case TypePing:
		return s.send(ctx, Envelope{Type: TypePong})

	case TypeUserMessage:
		var msg UserMessage
		if err := json.Unmarshal(env.Payload, &msg); err != nil {
			return s.SendError(ctx, "malformed user_message payload")
		}
		// Token size is logged on every message from day one (planv2 §1.6) so
		// the diffing work at §5.3 is driven by data, not speculation.
		h.log.Info("user message",
			"session", s.id,
			"chars", len(msg.Text),
			"canvas_bytes", len(msg.CanvasContext),
			"canvas_tokens_est", len(msg.CanvasContext)/4,
		)
		as, _ := s.takeAgent()
		if as == nil {
			return s.SendError(ctx, "no agent is running for this session")
		}
		if err := as.SendTurn(ctx, msg.Text, msg.CanvasContext); err != nil {
			h.log.Error("send turn failed", "err", err, "session", s.id)
			return s.SendError(ctx, err.Error())
		}
		return nil

	case TypeToolResult:
		var res ToolResult
		if err := json.Unmarshal(env.Payload, &res); err != nil {
			return s.SendError(ctx, "malformed tool_result payload")
		}
		s.executor.deliver(res)
		return nil

	case TypeCancel:
		// Ask the agent to stop first: an agent whose loop we own would otherwise
		// keep issuing tool calls after the UI has moved on. Claude Code's Cancel
		// is a no-op — its --max-turns bounds the subprocess — so for that agent
		// this remains just the turn_end the UI is waiting for.
		h.log.Info("cancel requested", "session", s.id)
		if as, _ := s.takeAgent(); as != nil {
			as.Cancel()
		}
		return s.SendTurnEnd(ctx)

	case TypeSwitchAgent:
		var msg SwitchAgent
		if err := json.Unmarshal(env.Payload, &msg); err != nil {
			return s.SendError(ctx, "malformed switch_agent payload")
		}
		_, currentName := s.takeAgent()
		if msg.Name == currentName {
			return s.sendTyped(ctx, TypeAgentSwitched, AgentSwitched{Current: currentName})
		}
		f := h.factoryNamed(msg.Name)
		if f == nil {
			return s.SendError(ctx, "unknown agent: "+msg.Name)
		}

		next, err := f.New(ctx, s.id, s.executor)
		if err != nil {
			h.log.Error("agent switch failed", "err", err, "session", s.id, "to", msg.Name)
			// Keep the working agent rather than leaving the session dead.
			return s.SendError(ctx, "could not start "+msg.Name+": "+err.Error())
		}

		// Close the outgoing agent only after the new one is live, so a failed
		// switch cannot strand the session with no agent at all. Closing it ends
		// its forwardEvents goroutine.
		s.closeAgent()
		s.setAgent(next, f.Name())
		go h.forwardEvents(ctx, s, next)

		h.log.Info("agent switched", "session", s.id, "from", currentName, "to", f.Name())
		return s.sendTyped(ctx, TypeAgentSwitched, AgentSwitched{Current: f.Name()})

	default:
		return s.SendError(ctx, "unknown message type: "+env.Type)
	}
}

func newSessionID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Only used for routing; a timestamp is an acceptable fallback.
		return "s" + time.Now().Format("20060102150405.000000")
	}
	return hex.EncodeToString(b)
}

// session is one live connection. It implements the send half of the protocol.
//
// Writes are serialised through writeMu: the read loop, the event forwarder,
// and any in-flight tool call can all reach the socket.
type session struct {
	conn     *websocket.Conn
	log      *slog.Logger
	id       string
	executor *browserExecutor

	writeMu sync.Mutex

	// agentMu guards the current agent, which switch_agent can replace while a
	// forwardEvents goroutine is still draining the outgoing one.
	agentMu   sync.Mutex
	agent     agent.AgentSession
	agentName string
}

func (s *session) setAgent(as agent.AgentSession, name string) {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	s.agent, s.agentName = as, name
}

// takeAgent returns the live agent for one turn. Callers must not hold the lock
// across a turn: SendTurn returns immediately, but a tool call inside it blocks
// on the browser, and the read loop needs this lock to service the result.
func (s *session) takeAgent() (agent.AgentSession, string) {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	return s.agent, s.agentName
}

func (s *session) closeAgent() {
	s.agentMu.Lock()
	as := s.agent
	s.agent = nil
	s.agentMu.Unlock()
	if as != nil {
		as.Close()
	}
}

func (s *session) SendDelta(ctx context.Context, text string) error {
	return s.sendTyped(ctx, TypeAssistantDelta, AssistantDelta{Text: text})
}

func (s *session) SendToolCall(ctx context.Context, call ToolCall) error {
	return s.sendTyped(ctx, TypeToolCall, call)
}

func (s *session) SendTurnEnd(ctx context.Context) error {
	return s.send(ctx, Envelope{Type: TypeTurnEnd})
}

func (s *session) SendError(ctx context.Context, message string) error {
	return s.sendTyped(ctx, TypeError, ErrorPayload{Message: message})
}

func (s *session) sendTyped(ctx context.Context, msgType string, payload any) error {
	env, err := newEnvelope(msgType, payload)
	if err != nil {
		return err
	}
	return s.send(ctx, env)
}

func (s *session) send(ctx context.Context, env Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	// Concurrent writes to a websocket corrupt the frame stream.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.conn.Write(ctx, websocket.MessageText, data)
}
