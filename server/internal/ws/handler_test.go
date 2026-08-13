package ws

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func (s *toolSession) Cancel() {}

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

// namedFactory is a minimal agent that reports which factory produced it, so a
// test can prove a switch changed who answers.
type namedFactory struct {
	name  string
	fail  bool
	mu    sync.Mutex
	built int
}

func (f *namedFactory) Name() string { return f.name }

func (f *namedFactory) New(_ context.Context, _ string, _ agent.ToolExecutor) (agent.AgentSession, error) {
	if f.fail {
		return nil, io.ErrUnexpectedEOF
	}
	f.mu.Lock()
	f.built++
	f.mu.Unlock()
	return &namedSession{
		name:   f.name,
		events: make(chan agent.AgentEvent, 8),
		closed: make(chan struct{}),
	}, nil
}

func (f *namedFactory) builds() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.built
}

type namedSession struct {
	name   string
	events chan agent.AgentEvent
	closed chan struct{}
	once   sync.Once
}

func (s *namedSession) SendTurn(_ context.Context, _ string, _ json.RawMessage) error {
	go func() {
		s.emit(agent.AgentEvent{Type: agent.EventTextDelta, Text: "from:" + s.name})
		s.emit(agent.AgentEvent{Type: agent.EventTurnDone})
	}()
	return nil
}

func (s *namedSession) emit(ev agent.AgentEvent) {
	select {
	case s.events <- ev:
	case <-s.closed:
	}
}

func (s *namedSession) Events() <-chan agent.AgentEvent { return s.events }

func (s *namedSession) Cancel() {}

// Close deliberately does not close events: a turn goroutine may still be
// writing to it, exactly like the real agents.
func (s *namedSession) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

// dialTest opens a client socket against a handler and returns it plus cleanup.
func dialTest(t *testing.T, h *Handler) (*websocket.Conn, func()) {
	t.Helper()
	srv := httptest.NewServer(h)
	conn, _, err := websocket.Dial(context.Background(), "ws"+srv.URL[4:], nil)
	if err != nil {
		srv.Close()
		t.Fatalf("dial: %v", err)
	}
	return conn, func() { conn.CloseNow(); srv.Close() }
}

func readEnvelope(t *testing.T, conn *websocket.Conn) Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
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

func writeJSON(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// turnText sends a user_message and returns the assembled assistant text.
func turnText(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	writeJSON(t, conn, map[string]any{
		"type":    TypeUserMessage,
		"payload": map[string]any{"text": "hi"},
	})
	var text string
	for {
		env := readEnvelope(t, conn)
		switch env.Type {
		case TypeAssistantDelta:
			var d AssistantDelta
			json.Unmarshal(env.Payload, &d)
			text += d.Text
		case TypeTurnEnd:
			return text
		case TypeError:
			return "ERR:" + string(env.Payload)
		}
	}
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestAgentListIsSentOnConnect(t *testing.T) {
	h := NewHandlerWithAgents([]agent.Factory{
		&namedFactory{name: "alpha"}, &namedFactory{name: "beta"},
	}, quiet(), []string{"*"})
	conn, done := dialTest(t, h)
	defer done()

	env := readEnvelope(t, conn)
	if env.Type != TypeAgentsAvailable {
		t.Fatalf("first frame = %q, want %q", env.Type, TypeAgentsAvailable)
	}
	var av AgentsAvailable
	json.Unmarshal(env.Payload, &av)
	if len(av.Names) != 2 || av.Names[0] != "alpha" {
		t.Errorf("names = %v", av.Names)
	}
	if av.Current != "alpha" {
		t.Errorf("current = %q, want the first factory", av.Current)
	}
}

// The point of the feature: after switching, a different agent answers.
func TestSwitchAgentChangesWhoAnswers(t *testing.T) {
	h := NewHandlerWithAgents([]agent.Factory{
		&namedFactory{name: "alpha"}, &namedFactory{name: "beta"},
	}, quiet(), []string{"*"})
	conn, done := dialTest(t, h)
	defer done()
	readEnvelope(t, conn) // agents_available

	if got := turnText(t, conn); got != "from:alpha" {
		t.Fatalf("before switch = %q", got)
	}

	writeJSON(t, conn, map[string]any{
		"type": TypeSwitchAgent, "payload": map[string]any{"name": "beta"},
	})
	if env := readEnvelope(t, conn); env.Type != TypeAgentSwitched {
		t.Fatalf("switch reply = %q, want %q", env.Type, TypeAgentSwitched)
	}

	if got := turnText(t, conn); got != "from:beta" {
		t.Errorf("after switch = %q, want %q", got, "from:beta")
	}
}

// A switch must not leave two forwarders writing to one socket. The outgoing
// agent's events are dropped rather than interleaved into the new conversation.
func TestOldAgentEventsAreDroppedAfterSwitch(t *testing.T) {
	alpha := &namedFactory{name: "alpha"}
	h := NewHandlerWithAgents([]agent.Factory{
		alpha, &namedFactory{name: "beta"},
	}, quiet(), []string{"*"})
	conn, done := dialTest(t, h)
	defer done()
	readEnvelope(t, conn)

	turnText(t, conn) // establish alpha
	writeJSON(t, conn, map[string]any{
		"type": TypeSwitchAgent, "payload": map[string]any{"name": "beta"},
	})
	readEnvelope(t, conn)

	// Every delta from here must come from beta.
	for i := 0; i < 3; i++ {
		if got := turnText(t, conn); got != "from:beta" {
			t.Fatalf("turn %d = %q, want only beta's text", i, got)
		}
	}
}

func TestSwitchToUnknownAgentErrorsAndKeepsWorking(t *testing.T) {
	h := NewHandlerWithAgents([]agent.Factory{
		&namedFactory{name: "alpha"}, &namedFactory{name: "beta"},
	}, quiet(), []string{"*"})
	conn, done := dialTest(t, h)
	defer done()
	readEnvelope(t, conn)

	writeJSON(t, conn, map[string]any{
		"type": TypeSwitchAgent, "payload": map[string]any{"name": "nope"},
	})
	env := readEnvelope(t, conn)
	if env.Type != TypeError {
		t.Fatalf("reply = %q, want %q", env.Type, TypeError)
	}
	// The original agent must still be live.
	if got := turnText(t, conn); got != "from:alpha" {
		t.Errorf("after bad switch = %q, want alpha still answering", got)
	}
}

// A failed switch must not strand the session with no agent at all.
func TestFailedSwitchKeepsTheWorkingAgent(t *testing.T) {
	h := NewHandlerWithAgents([]agent.Factory{
		&namedFactory{name: "alpha"}, &namedFactory{name: "broken", fail: true},
	}, quiet(), []string{"*"})
	conn, done := dialTest(t, h)
	defer done()
	readEnvelope(t, conn)

	writeJSON(t, conn, map[string]any{
		"type": TypeSwitchAgent, "payload": map[string]any{"name": "broken"},
	})
	if env := readEnvelope(t, conn); env.Type != TypeError {
		t.Fatalf("reply = %q, want an error", env.Type)
	}
	if got := turnText(t, conn); got != "from:alpha" {
		t.Errorf("after failed switch = %q, want alpha still answering", got)
	}
}

// Switching to the current agent should be a no-op, not a needless restart:
// restarting Claude Code costs a subprocess spawn.
func TestSwitchToSameAgentDoesNotRestartIt(t *testing.T) {
	alpha := &namedFactory{name: "alpha"}
	h := NewHandlerWithAgents([]agent.Factory{alpha, &namedFactory{name: "beta"}}, quiet(), []string{"*"})
	conn, done := dialTest(t, h)
	defer done()
	readEnvelope(t, conn)

	before := alpha.builds()
	writeJSON(t, conn, map[string]any{
		"type": TypeSwitchAgent, "payload": map[string]any{"name": "alpha"},
	})
	if env := readEnvelope(t, conn); env.Type != TypeAgentSwitched {
		t.Fatalf("reply = %q", env.Type)
	}
	if after := alpha.builds(); after != before {
		t.Errorf("alpha rebuilt %d -> %d; a same-agent switch should be a no-op", before, after)
	}
}

// promptFactory records the prompt each session was built with, so a test can
// prove a skill change actually reached the agent.
type promptFactory struct {
	name string
	mu   sync.Mutex
	seen []string
}

func (f *promptFactory) Name() string { return f.name }

func (f *promptFactory) WithPrompt(p string) agent.Factory {
	return &promptChild{parent: f, prompt: p, name: f.name}
}

func (f *promptFactory) New(_ context.Context, _ string, _ agent.ToolExecutor) (agent.AgentSession, error) {
	return newNamedSession(f.name), nil
}

func (f *promptFactory) prompts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

type promptChild struct {
	parent *promptFactory
	prompt string
	name   string
}

func (c *promptChild) Name() string { return c.name }

func (c *promptChild) WithPrompt(p string) agent.Factory {
	return &promptChild{parent: c.parent, prompt: p, name: c.name}
}

func (c *promptChild) New(_ context.Context, _ string, _ agent.ToolExecutor) (agent.AgentSession, error) {
	c.parent.mu.Lock()
	c.parent.seen = append(c.parent.seen, c.prompt)
	c.parent.mu.Unlock()
	return newNamedSession(c.name), nil
}

func newNamedSession(name string) *namedSession {
	return &namedSession{
		name:   name,
		events: make(chan agent.AgentEvent, 8),
		closed: make(chan struct{}),
	}
}

func skillHandler(t *testing.T) (*Handler, *promptFactory, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := agent.NewSkillStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := &promptFactory{name: "alpha"}
	h := NewHandlerWithAgents([]agent.Factory{f}, quiet(), []string{"*"}).WithSkills(store)
	return h, f, dir
}

func readUntil(t *testing.T, conn *websocket.Conn, want string) Envelope {
	t.Helper()
	for i := 0; i < 10; i++ {
		env := readEnvelope(t, conn)
		if env.Type == want {
			return env
		}
	}
	t.Fatalf("never saw a %q frame", want)
	return Envelope{}
}

func TestSkillsStateSentOnConnect(t *testing.T) {
	h, _, _ := skillHandler(t)
	conn, done := dialTest(t, h)
	defer done()

	env := readUntil(t, conn, TypeSkillsState)
	var st SkillsState
	if err := json.Unmarshal(env.Payload, &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Skills) == 0 {
		t.Error("no skills offered")
	}
	if len(st.Enabled) != 0 {
		t.Errorf("a new session should start with no optional skills, got %v", st.Enabled)
	}
	if st.PromptTokens == 0 || st.CanvasBudget == 0 {
		t.Error("token costs should be reported so the picker can show them")
	}
	for _, sk := range st.Skills {
		if sk.Name == "" || sk.Description == "" {
			t.Errorf("%s is missing display metadata", sk.ID)
		}
	}
}

// The point of the feature: enabling a skill changes what the agent is told.
func TestEnablingASkillChangesTheAgentPrompt(t *testing.T) {
	h, f, _ := skillHandler(t)
	conn, done := dialTest(t, h)
	defer done()
	readUntil(t, conn, TypeSkillsState)

	before := f.prompts()
	if len(before) == 0 {
		t.Fatal("the initial session was not built through WithPrompt")
	}
	if strings.Contains(before[0], "Flow chart conventions") {
		t.Fatal("an unselected skill is already in the prompt")
	}

	writeJSON(t, conn, map[string]any{
		"type": TypeSetSkills, "payload": map[string]any{"enabled": []string{"flowcharts"}},
	})
	readUntil(t, conn, TypeSkillsState)

	after := f.prompts()
	if len(after) <= len(before) {
		t.Fatal("the agent was not rebuilt after a skill change")
	}
	latest := after[len(after)-1]
	if !strings.Contains(latest, "Flow chart conventions") {
		t.Error("the enabled skill did not reach the agent's prompt")
	}
	// The core skill must survive alongside it.
	if !strings.Contains(latest, "never emit pixels") {
		t.Error("the core canvas skill was lost when a skill was enabled")
	}
}

func TestDisablingASkillRemovesItFromThePrompt(t *testing.T) {
	h, f, _ := skillHandler(t)
	conn, done := dialTest(t, h)
	defer done()
	readUntil(t, conn, TypeSkillsState)

	writeJSON(t, conn, map[string]any{
		"type": TypeSetSkills, "payload": map[string]any{"enabled": []string{"flowcharts"}},
	})
	readUntil(t, conn, TypeSkillsState)
	writeJSON(t, conn, map[string]any{
		"type": TypeSetSkills, "payload": map[string]any{"enabled": []string{}},
	})
	readUntil(t, conn, TypeSkillsState)

	latest := f.prompts()[len(f.prompts())-1]
	if strings.Contains(latest, "Flow chart conventions") {
		t.Error("a disabled skill is still in the prompt")
	}
	if !strings.Contains(latest, "never emit pixels") {
		t.Error("the core skill should always remain")
	}
}

func TestSaveAndDeleteSkillOverTheWire(t *testing.T) {
	h, _, dir := skillHandler(t)
	conn, done := dialTest(t, h)
	defer done()
	readUntil(t, conn, TypeSkillsState)

	writeJSON(t, conn, map[string]any{
		"type": TypeSaveSkill,
		"payload": map[string]any{
			"id":   "terse",
			"body": "# Terse\n\nOne sentence answers only.",
		},
	})
	env := readUntil(t, conn, TypeSkillsState)
	var st SkillsState
	json.Unmarshal(env.Payload, &st)

	var found *SkillInfo
	for i := range st.Skills {
		if st.Skills[i].ID == "terse" {
			found = &st.Skills[i]
		}
	}
	if found == nil {
		t.Fatal("the saved skill is not in the state")
	}
	if found.BuiltIn {
		t.Error("a user skill must not be marked built in")
	}
	if _, err := os.Stat(filepath.Join(dir, "terse.md")); err != nil {
		t.Errorf("the skill was not written to disk: %v", err)
	}

	writeJSON(t, conn, map[string]any{
		"type": TypeDeleteSkill, "payload": map[string]any{"id": "terse"},
	})
	env = readUntil(t, conn, TypeSkillsState)
	json.Unmarshal(env.Payload, &st)
	for _, sk := range st.Skills {
		if sk.ID == "terse" {
			t.Error("the deleted skill is still listed")
		}
	}
}

// Deleting an enabled skill must drop it from the selection too, or the prompt
// would keep citing something that no longer exists.
func TestDeletingAnEnabledSkillDisablesIt(t *testing.T) {
	h, f, _ := skillHandler(t)
	conn, done := dialTest(t, h)
	defer done()
	readUntil(t, conn, TypeSkillsState)

	writeJSON(t, conn, map[string]any{
		"type":    TypeSaveSkill,
		"payload": map[string]any{"id": "temp", "body": "# Temp\n\nMARKER-TEXT here."},
	})
	readUntil(t, conn, TypeSkillsState)
	writeJSON(t, conn, map[string]any{
		"type": TypeSetSkills, "payload": map[string]any{"enabled": []string{"temp"}},
	})
	readUntil(t, conn, TypeSkillsState)
	if !strings.Contains(f.prompts()[len(f.prompts())-1], "MARKER-TEXT") {
		t.Fatal("the skill never reached the prompt")
	}

	writeJSON(t, conn, map[string]any{
		"type": TypeDeleteSkill, "payload": map[string]any{"id": "temp"},
	})
	env := readUntil(t, conn, TypeSkillsState)
	var st SkillsState
	json.Unmarshal(env.Payload, &st)
	for _, id := range st.Enabled {
		if id == "temp" {
			t.Error("the deleted skill is still enabled")
		}
	}
	if strings.Contains(f.prompts()[len(f.prompts())-1], "MARKER-TEXT") {
		t.Error("the deleted skill is still in the prompt")
	}
}

func TestBuiltInCannotBeOverwrittenOverTheWire(t *testing.T) {
	h, _, _ := skillHandler(t)
	conn, done := dialTest(t, h)
	defer done()
	readUntil(t, conn, TypeSkillsState)

	writeJSON(t, conn, map[string]any{
		"type":    TypeSaveSkill,
		"payload": map[string]any{"id": "flowcharts", "body": "# Hijacked\n\nnope"},
	})
	if env := readUntil(t, conn, TypeError); env.Type != TypeError {
		t.Error("overwriting a built-in should be refused")
	}
}

// Skills must survive an agent switch, or picking a different model silently
// resets them.
func TestSkillsSurviveAnAgentSwitch(t *testing.T) {
	dir := t.TempDir()
	store, err := agent.NewSkillStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &promptFactory{name: "alpha"}
	b := &promptFactory{name: "beta"}
	h := NewHandlerWithAgents([]agent.Factory{a, b}, quiet(), []string{"*"}).WithSkills(store)

	conn, done := dialTest(t, h)
	defer done()
	readUntil(t, conn, TypeSkillsState)

	writeJSON(t, conn, map[string]any{
		"type": TypeSetSkills, "payload": map[string]any{"enabled": []string{"flowcharts"}},
	})
	readUntil(t, conn, TypeSkillsState)

	writeJSON(t, conn, map[string]any{
		"type": TypeSwitchAgent, "payload": map[string]any{"name": "beta"},
	})
	readUntil(t, conn, TypeAgentSwitched)

	got := b.prompts()
	if len(got) == 0 {
		t.Fatal("beta was never built with a prompt")
	}
	if !strings.Contains(got[len(got)-1], "Flow chart conventions") {
		t.Error("the skill selection did not carry across the agent switch")
	}
}

// Without a skill store the handler must behave exactly as it did before skills
// existed: no skills_state frame, and set_skills does not break the session.
func TestNoSkillStoreStillWorks(t *testing.T) {
	f := &promptFactory{name: "alpha"}
	h := NewHandlerWithAgents([]agent.Factory{f}, quiet(), []string{"*"})
	conn, done := dialTest(t, h)
	defer done()

	readUntil(t, conn, TypeAgentsAvailable)
	writeJSON(t, conn, map[string]any{
		"type": TypeSetSkills, "payload": map[string]any{"enabled": []string{"flowcharts"}},
	})
	// The session must still answer a turn afterwards.
	if got := turnText(t, conn); got != "from:alpha" {
		t.Errorf("session broken after set_skills with no store: %q", got)
	}
}

// Go marshals a nil slice as JSON null, and the browser then threw on
// null.length — which crashed the skills panel, the chat panel and the whole
// page. The Go tests could not see it because []string(nil) and []string{} are
// equal in Go; only the JSON differs. So assert the JSON.
func TestSkillsStateNeverSendsNullArrays(t *testing.T) {
	h, _, _ := skillHandler(t)
	conn, done := dialTest(t, h)
	defer done()

	env := readUntil(t, conn, TypeSkillsState)
	raw := string(env.Payload)
	if strings.Contains(raw, `"enabled":null`) {
		t.Errorf("enabled is null; the browser cannot read .length off it: %s", raw)
	}
	if strings.Contains(raw, `"skills":null`) {
		t.Errorf("skills is null: %s", raw)
	}

	// And decoding into a map must give real arrays, not nil.
	var got map[string]any
	if err := json.Unmarshal(env.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["enabled"].([]any); !ok {
		t.Errorf("enabled decoded as %T, want an array", got["enabled"])
	}
}
