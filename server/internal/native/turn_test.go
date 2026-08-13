package native

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anubhavitis/whiteboard/server/internal/agent"
)

// --- fakes -----------------------------------------------------------------

// toolFrame renders one complete tool call as mlx_lm emits it: name and full
// arguments in a single SSE frame (verified against mlx_lm 0.31.3).
func toolFrame(idx int, id, name, args string) string {
	argsJSON, _ := json.Marshal(args)
	return fmt.Sprintf(
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":%d,"id":%q,"type":"function","function":{"name":%q,"arguments":%s}}]}}]}`,
		idx, id, name, argsJSON) + "\n\n"
}

func textFrame(s string) string {
	b, _ := json.Marshal(s)
	return fmt.Sprintf(`data: {"choices":[{"delta":{"content":%s}}]}`, b) + "\n\n"
}

func finishFrame(reason string) string {
	return fmt.Sprintf(`data: {"choices":[{"delta":{},"finish_reason":%q}]}`, reason) + "\n\n"
}

// scriptedServer replies with a canned SSE response per request, recording the
// request bodies so a test can assert what the model was told.
type scriptedServer struct {
	mu       sync.Mutex
	replies  []string
	bodies   [][]byte
	requests int
}

func (s *scriptedServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)

		s.mu.Lock()
		s.bodies = append(s.bodies, body)
		i := s.requests
		s.requests++
		var reply string
		if i < len(s.replies) {
			reply = s.replies[i]
		} else {
			// Past the script: a plain closing sentence, so a runaway loop ends.
			reply = textFrame("done") + finishFrame("stop")
		}
		s.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, reply)
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}
}

func (s *scriptedServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func (s *scriptedServer) body(i int) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out map[string]any
	json.Unmarshal(s.bodies[i], &out)
	return out
}

// fakeExecutor records invocations and replies from a script.
type fakeExecutor struct {
	mu       sync.Mutex
	calls    []agent.ToolInvocation
	replies  map[string]agent.ToolOutcome // by tool name
	fallback agent.ToolOutcome
	block    chan struct{}
}

func (e *fakeExecutor) Execute(ctx context.Context, call agent.ToolInvocation) agent.ToolOutcome {
	if e.block != nil {
		select {
		case <-e.block:
		case <-ctx.Done():
			return agent.ToolOutcome{OK: false, Error: "cancelled"}
		}
	}
	e.mu.Lock()
	e.calls = append(e.calls, call)
	out, ok := e.replies[call.Name]
	e.mu.Unlock()
	if !ok {
		return e.fallback
	}
	return out
}

func (e *fakeExecutor) invocations() []agent.ToolInvocation {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]agent.ToolInvocation(nil), e.calls...)
}

// newToolSession wires a session against a scripted server and executor.
func newToolSession(t *testing.T, script *scriptedServer, exec agent.ToolExecutor) (*Session, func()) {
	t.Helper()
	srv := httptest.NewServer(script.handler(t))
	f := NewFactory(srv.URL+"/v1", "test-model", "SYS", quietLog())
	as, err := f.New(context.Background(), "sess", exec)
	if err != nil {
		t.Fatal(err)
	}
	return as.(*Session), func() { as.Close(); srv.Close() }
}

// drain collects events until turn_done.
type drained struct {
	text      string
	errors    []string
	toolCalls []string
	toolDone  int
	turnDone  int
}

func drain(t *testing.T, s *Session) drained {
	t.Helper()
	var d drained
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			switch ev.Type {
			case agent.EventTextDelta:
				d.text += ev.Text
			case agent.EventError:
				d.errors = append(d.errors, ev.Text)
			case agent.EventToolCall:
				d.toolCalls = append(d.toolCalls, ev.ToolName)
			case agent.EventToolDone:
				d.toolDone++
			case agent.EventTurnDone:
				d.turnDone++
				return d
			}
		case <-deadline:
			t.Fatalf("timed out; got so far: %+v", d)
		}
	}
}

// --- tests -----------------------------------------------------------------

// The core loop: a tool call runs, and the model's next request carries the
// result as a role:"tool" message with the browser's real shape id.
func TestToolResultReachesTheModelWithTheRealID(t *testing.T) {
	script := &scriptedServer{replies: []string{
		toolFrame(0, "call_1", "create_shape", `{"shape":"box","text":"Postgres","near":"shape:auth","direction":"below"}`) +
			finishFrame("tool_calls"),
		textFrame("Added it.") + finishFrame("stop"),
	}}
	exec := &fakeExecutor{replies: map[string]agent.ToolOutcome{
		"create_shape": {OK: true, ShapeIDs: []string{"shape:REAL9"}},
	}}
	s, done := newToolSession(t, script, exec)
	defer done()

	if err := s.SendTurn(context.Background(), "add a db", nil); err != nil {
		t.Fatal(err)
	}
	d := drain(t, s)

	if d.turnDone != 1 {
		t.Errorf("turn_done fired %d times, want 1", d.turnDone)
	}
	if d.text != "Added it." {
		t.Errorf("text = %q", d.text)
	}
	if len(exec.invocations()) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(exec.invocations()))
	}
	if script.count() != 2 {
		t.Fatalf("model calls = %d, want 2 (call, then summary)", script.count())
	}

	// The second request must replay the assistant tool_calls and the result.
	msgs := messagesOf(t, script.body(1))
	var sawAssistantCall, sawToolResult bool
	for _, m := range msgs {
		if m["role"] == "assistant" && m["tool_calls"] != nil {
			sawAssistantCall = true
		}
		if m["role"] == "tool" {
			sawToolResult = true
			body, _ := m["content"].(string)
			if !strings.Contains(body, "shape:REAL9") {
				t.Errorf("tool result must state the real id, got %q", body)
			}
			if m["tool_call_id"] != "call_1" {
				t.Errorf("tool_call_id = %v, want call_1", m["tool_call_id"])
			}
		}
	}
	if !sawAssistantCall {
		t.Error("assistant message with tool_calls was not replayed")
	}
	if !sawToolResult {
		t.Error("no role:\"tool\" message in the follow-up request")
	}
}

func messagesOf(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, _ := body["messages"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		if mm, ok := m.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

// Tools must be sent in the nested OpenAI shape, or Qwen3's chat template
// renders nothing and the model never learns they exist.
func TestToolsAreSentInTheOpenAIShape(t *testing.T) {
	script := &scriptedServer{replies: []string{textFrame("hi") + finishFrame("stop")}}
	s, done := newToolSession(t, script, &fakeExecutor{})
	defer done()

	s.SendTurn(context.Background(), "hello", nil)
	drain(t, s)

	tools, _ := script.body(0)["tools"].([]any)
	if len(tools) != len(agent.Tools()) {
		t.Fatalf("sent %d tools, want %d", len(tools), len(agent.Tools()))
	}
	first, _ := tools[0].(map[string]any)
	if first["type"] != "function" {
		t.Errorf(`type = %v, want "function"`, first["type"])
	}
	fn, ok := first["function"].(map[string]any)
	if !ok {
		t.Fatalf("function is %T, want an object", first["function"])
	}
	if _, ok := fn["parameters"]; !ok {
		t.Error("function.parameters missing")
	}
}

// A chat-only session (no executor) must not advertise tools.
func TestNoExecutorMeansNoTools(t *testing.T) {
	script := &scriptedServer{replies: []string{textFrame("hi") + finishFrame("stop")}}
	s, done := newToolSession(t, script, nil)
	defer done()

	s.SendTurn(context.Background(), "hello", nil)
	drain(t, s)

	if _, present := script.body(0)["tools"]; present {
		t.Error("a session with no executor must not send tools")
	}
}

// Two calls in one response run in order — create_shape before the create_arrow
// that will reference it.
func TestTwoCallsInOneResponseRunInOrder(t *testing.T) {
	script := &scriptedServer{replies: []string{
		toolFrame(0, "c1", "create_shape", `{"shape":"box","text":"PG"}`) +
			toolFrame(1, "c2", "create_arrow", `{"from_id":"shape:auth","to_id":"shape:guess"}`) +
			finishFrame("tool_calls"),
		textFrame("done") + finishFrame("stop"),
	}}
	exec := &fakeExecutor{replies: map[string]agent.ToolOutcome{
		"create_shape": {OK: true, ShapeIDs: []string{"shape:R1"}},
		"create_arrow": {OK: false, Error: "no shape with id shape:guess"},
	}}
	s, done := newToolSession(t, script, exec)
	defer done()

	s.SendTurn(context.Background(), "add and connect", nil)
	d := drain(t, s)

	got := exec.invocations()
	if len(got) != 2 {
		t.Fatalf("ran %d calls, want 2", len(got))
	}
	if got[0].Name != "create_shape" || got[1].Name != "create_arrow" {
		t.Errorf("order = %s,%s", got[0].Name, got[1].Name)
	}
	if d.toolDone != 2 {
		t.Errorf("tool_done events = %d, want 2", d.toolDone)
	}
}

// The self-correction path: a failed arrow's error must reach the model verbatim,
// because it names the id that did not exist.
func TestToolFailureTextReachesTheModel(t *testing.T) {
	script := &scriptedServer{replies: []string{
		toolFrame(0, "c1", "create_arrow", `{"from_id":"shape:a","to_id":"shape:ghost"}`) +
			finishFrame("tool_calls"),
		textFrame("sorry") + finishFrame("stop"),
	}}
	exec := &fakeExecutor{replies: map[string]agent.ToolOutcome{
		"create_arrow": {OK: false, Error: "no shape with id shape:ghost"},
	}}
	s, done := newToolSession(t, script, exec)
	defer done()

	s.SendTurn(context.Background(), "connect them", nil)
	drain(t, s)

	var found bool
	for _, m := range messagesOf(t, script.body(1)) {
		if m["role"] == "tool" && strings.Contains(m["content"].(string), "shape:ghost") {
			found = true
		}
	}
	if !found {
		t.Error("the browser's error must be handed to the model so it can correct itself")
	}
}

// Malformed arguments never reach the browser; the model gets a fixable error.
func TestMalformedArgumentsDoNotReachTheBrowser(t *testing.T) {
	// arguments is not valid JSON.
	bad := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"create_shape","arguments":"{broken"}}]}}]}` + "\n\n"
	script := &scriptedServer{replies: []string{
		bad + finishFrame("tool_calls"),
		textFrame("ok") + finishFrame("stop"),
	}}
	exec := &fakeExecutor{}
	s, done := newToolSession(t, script, exec)
	defer done()

	s.SendTurn(context.Background(), "draw", nil)
	drain(t, s)

	if n := len(exec.invocations()); n != 0 {
		t.Errorf("executor ran %d times, want 0", n)
	}
	var found bool
	for _, m := range messagesOf(t, script.body(1)) {
		if m["role"] == "tool" && strings.Contains(m["content"].(string), "not valid JSON") {
			found = true
		}
	}
	if !found {
		t.Error("the model should be told its arguments were malformed")
	}
}

func TestUnknownToolNameIsRejectedInGo(t *testing.T) {
	script := &scriptedServer{replies: []string{
		toolFrame(0, "c1", "make_sandwich", `{}`) + finishFrame("tool_calls"),
		textFrame("ok") + finishFrame("stop"),
	}}
	exec := &fakeExecutor{}
	s, done := newToolSession(t, script, exec)
	defer done()

	s.SendTurn(context.Background(), "draw", nil)
	drain(t, s)

	if n := len(exec.invocations()); n != 0 {
		t.Errorf("executor ran %d times, want 0 — unknown tools are caught in Go", n)
	}
	var found bool
	for _, m := range messagesOf(t, script.body(1)) {
		if m["role"] == "tool" && strings.Contains(m["content"].(string), "unknown tool") {
			found = true
		}
	}
	if !found {
		t.Error("the model should be told the tool does not exist")
	}
}

// The cap is the real bound on a runaway loop.
func TestToolCallCapIsEnforced(t *testing.T) {
	// Every reply asks for another tool, forever.
	forever := make([]string, 40)
	for i := range forever {
		forever[i] = toolFrame(0, fmt.Sprintf("c%d", i), "create_shape",
			fmt.Sprintf(`{"shape":"box","text":"n%d"}`, i)) + finishFrame("tool_calls")
	}
	script := &scriptedServer{replies: forever}
	exec := &fakeExecutor{fallback: agent.ToolOutcome{OK: true, ShapeIDs: []string{"shape:x"}}}
	s, done := newToolSession(t, script, exec)
	defer done()

	s.SendTurn(context.Background(), "draw forever", nil)
	d := drain(t, s)

	if n := len(exec.invocations()); n != agent.MaxToolCallsPerTurn {
		t.Errorf("ran %d tools, want exactly the cap %d", n, agent.MaxToolCallsPerTurn)
	}
	if d.turnDone != 1 {
		t.Errorf("turn_done = %d, want 1", d.turnDone)
	}
	var capped bool
	for _, e := range d.errors {
		if strings.Contains(e, "stopped after") {
			capped = true
		}
	}
	if !capped {
		t.Errorf("the user must be told the cap was hit; errors = %v", d.errors)
	}
	// The final call must withhold tools so the model closes off rather than
	// asking for more.
	last := script.count() - 1
	if _, present := script.body(last)["tools"]; present {
		t.Error("the post-cap call must omit tools; withholding them is the enforcement")
	}
}

// The same call repeated is a stuck model, not progress.
func TestRepeatedIdenticalCallIsBroken(t *testing.T) {
	same := toolFrame(0, "c", "create_arrow", `{"from_id":"a","to_id":"b"}`) + finishFrame("tool_calls")
	script := &scriptedServer{replies: []string{same, same, same, same,
		textFrame("giving up") + finishFrame("stop")}}
	exec := &fakeExecutor{fallback: agent.ToolOutcome{OK: false, Error: "nope"}}
	s, done := newToolSession(t, script, exec)
	defer done()

	s.SendTurn(context.Background(), "connect", nil)
	drain(t, s)

	// The third identical attempt is refused in Go, so the browser sees two.
	if n := len(exec.invocations()); n != repeatLimit-1 {
		t.Errorf("executor ran %d times, want %d before the repeat guard fires", n, repeatLimit-1)
	}
}

// Raw coordinates are stripped and the model is told, because the frontend
// ignores unknown keys — leaving them in would let the model believe it
// positioned something.
func TestCoordinatesAreStrippedAndWarned(t *testing.T) {
	script := &scriptedServer{replies: []string{
		toolFrame(0, "c1", "create_shape", `{"shape":"box","text":"PG","x":120,"y":40}`) +
			finishFrame("tool_calls"),
		textFrame("ok") + finishFrame("stop"),
	}}
	exec := &fakeExecutor{replies: map[string]agent.ToolOutcome{
		"create_shape": {OK: true, ShapeIDs: []string{"shape:R1"}},
	}}
	s, done := newToolSession(t, script, exec)
	defer done()

	s.SendTurn(context.Background(), "draw", nil)
	drain(t, s)

	got := exec.invocations()
	if len(got) != 1 {
		t.Fatalf("calls = %d", len(got))
	}
	var args map[string]any
	json.Unmarshal(got[0].Args, &args)
	if _, bad := args["x"]; bad {
		t.Error("x must be stripped before reaching the canvas")
	}
	if _, bad := args["y"]; bad {
		t.Error("y must be stripped before reaching the canvas")
	}
	if args["text"] != "PG" {
		t.Errorf("legitimate args were lost: %v", args)
	}
	var warned bool
	for _, m := range messagesOf(t, script.body(1)) {
		if m["role"] == "tool" && strings.Contains(m["content"].(string), "near and direction") {
			warned = true
		}
	}
	if !warned {
		t.Error("the model should be told coordinates were ignored")
	}
}

// finish_reason says tool_calls but nothing parsed: mlx_lm dropped a truncated
// call. Silence here would look like a turn that simply ended.
func TestTruncatedToolCallIsReported(t *testing.T) {
	script := &scriptedServer{replies: []string{finishFrame("tool_calls")}}
	s, done := newToolSession(t, script, &fakeExecutor{})
	defer done()

	s.SendTurn(context.Background(), "draw", nil)
	d := drain(t, s)

	var found bool
	for _, e := range d.errors {
		if strings.Contains(e, "cut off") {
			found = true
		}
	}
	if !found {
		t.Errorf("a dropped tool call must be reported; errors = %v", d.errors)
	}
	if d.turnDone != 1 {
		t.Errorf("turn_done = %d, want 1", d.turnDone)
	}
}

// Cancelling mid-tool must end quietly: it is the Stop button.
func TestCancelDuringToolCallEndsQuietly(t *testing.T) {
	script := &scriptedServer{replies: []string{
		toolFrame(0, "c1", "create_shape", `{"shape":"box","text":"PG"}`) + finishFrame("tool_calls"),
	}}
	block := make(chan struct{})
	exec := &fakeExecutor{block: block, fallback: agent.ToolOutcome{OK: true}}
	s, done := newToolSession(t, script, exec)
	defer done()
	defer close(block)

	s.SendTurn(context.Background(), "draw", nil)
	// Let the loop reach the blocked executor, then stop the turn.
	time.Sleep(150 * time.Millisecond)
	s.Cancel()

	d := drain(t, s)
	if d.turnDone != 1 {
		t.Errorf("turn_done = %d, want 1", d.turnDone)
	}
	for _, e := range d.errors {
		if strings.Contains(e, "could not be reached") {
			t.Errorf("cancellation reported as a failure: %q", e)
		}
	}
}

// Durable history must not keep tool traffic: canvas JSON is re-sent every turn
// and is the authority. An assistant message with tool_calls but no matching
// tool result would also be invalid on the wire.
func TestHistoryDropsToolTrafficBetweenTurns(t *testing.T) {
	script := &scriptedServer{replies: []string{
		toolFrame(0, "c1", "create_shape", `{"shape":"box","text":"PG"}`) + finishFrame("tool_calls"),
		textFrame("Added PG.") + finishFrame("stop"),
		textFrame("second turn") + finishFrame("stop"),
	}}
	exec := &fakeExecutor{replies: map[string]agent.ToolOutcome{
		"create_shape": {OK: true, ShapeIDs: []string{"shape:R1"}},
	}}
	s, done := newToolSession(t, script, exec)
	defer done()

	s.SendTurn(context.Background(), "add a db", nil)
	drain(t, s)
	s.SendTurn(context.Background(), "and now?", nil)
	drain(t, s)

	for _, m := range messagesOf(t, script.body(2)) {
		if m["role"] == "tool" {
			t.Error("tool results must not persist into the next turn")
		}
		if m["role"] == "assistant" && m["tool_calls"] != nil {
			t.Error("assistant tool_calls must not persist without their results")
		}
	}
	// The assistant's words do persist — that is the conversation.
	var kept bool
	for _, m := range messagesOf(t, script.body(2)) {
		if m["role"] == "assistant" && m["content"] == "Added PG." {
			kept = true
		}
	}
	if !kept {
		t.Error("the assistant's final text should stay in history")
	}
}

// Reasoning plus tool calls must not trip the "only thought, never answered"
// error: a drawing turn legitimately has no prose.
func TestReasoningWithToolCallsIsNotAnError(t *testing.T) {
	reasoning := `data: {"choices":[{"delta":{"reasoning":"thinking about it"}}]}` + "\n\n"
	script := &scriptedServer{replies: []string{
		reasoning + toolFrame(0, "c1", "create_shape", `{"shape":"box","text":"PG"}`) + finishFrame("tool_calls"),
		textFrame("Added.") + finishFrame("stop"),
	}}
	exec := &fakeExecutor{replies: map[string]agent.ToolOutcome{
		"create_shape": {OK: true, ShapeIDs: []string{"shape:R1"}},
	}}
	s, done := newToolSession(t, script, exec)
	defer done()

	s.SendTurn(context.Background(), "draw", nil)
	d := drain(t, s)

	for _, e := range d.errors {
		if strings.Contains(e, "thinking") {
			t.Errorf("spurious thinking error on a drawing turn: %q", e)
		}
	}
}

// The dormant fallback: markup in content, no tool_calls field.
func TestInlineToolCallFallback(t *testing.T) {
	inline := textFrame(`<tool_call>{"name":"create_shape","arguments":{"shape":"box","text":"PG"}}</tool_call>`)
	script := &scriptedServer{replies: []string{
		inline + finishFrame("stop"),
		textFrame("ok") + finishFrame("stop"),
	}}
	exec := &fakeExecutor{replies: map[string]agent.ToolOutcome{
		"create_shape": {OK: true, ShapeIDs: []string{"shape:R1"}},
	}}
	s, done := newToolSession(t, script, exec)
	defer done()

	s.SendTurn(context.Background(), "draw", nil)
	d := drain(t, s)

	if n := len(exec.invocations()); n != 1 {
		t.Fatalf("inline tool call was not executed; calls = %d", n)
	}
	if strings.Contains(d.text, "<tool_call>") {
		t.Errorf("markup leaked into the transcript: %q", d.text)
	}
}

// Arguments fragmented across frames (real OpenAI behaviour) must still
// assemble, even though mlx_lm 0.31.3 does not fragment.
func TestFragmentedArgumentsAssemble(t *testing.T) {
	f1 := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"create_shape","arguments":"{\"shape\":"}}]}}]}` + "\n\n"
	f2 := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"\"box\",\"text\":\"PG\"}"}}]}}]}` + "\n\n"
	script := &scriptedServer{replies: []string{
		f1 + f2 + finishFrame("tool_calls"),
		textFrame("ok") + finishFrame("stop"),
	}}
	exec := &fakeExecutor{replies: map[string]agent.ToolOutcome{
		"create_shape": {OK: true, ShapeIDs: []string{"shape:R1"}},
	}}
	s, done := newToolSession(t, script, exec)
	defer done()

	s.SendTurn(context.Background(), "draw", nil)
	drain(t, s)

	got := exec.invocations()
	if len(got) != 1 {
		t.Fatalf("calls = %d, want 1", len(got))
	}
	var args map[string]any
	if err := json.Unmarshal(got[0].Args, &args); err != nil {
		t.Fatalf("fragmented arguments did not assemble: %v", err)
	}
	if args["text"] != "PG" {
		t.Errorf("args = %v", args)
	}
}

// A call with no id still needs a unique one: ws/executor keys its waiters by id.
func TestMissingCallIDIsSynthesised(t *testing.T) {
	noID := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"create_shape","arguments":"{\"shape\":\"box\",\"text\":\"A\"}"}}]}}]}` + "\n\n"
	script := &scriptedServer{replies: []string{
		noID + finishFrame("tool_calls"),
		textFrame("ok") + finishFrame("stop"),
	}}
	exec := &fakeExecutor{replies: map[string]agent.ToolOutcome{
		"create_shape": {OK: true, ShapeIDs: []string{"shape:R1"}},
	}}
	s, done := newToolSession(t, script, exec)
	defer done()

	s.SendTurn(context.Background(), "draw", nil)
	drain(t, s)

	got := exec.invocations()
	if len(got) != 1 {
		t.Fatalf("calls = %d", len(got))
	}
	if got[0].ID == "" {
		t.Error("a tool call must always carry an id; the executor keys waiters by it")
	}
}

// A self-loop is never meaningful and the canvas draws it anyway, so the loop
// must refuse it. Measured: asked to branch a flow, the model emitted
// create_arrow{from_id: X, to_id: X} alongside the real arrows.
func TestSelfLoopArrowIsRefused(t *testing.T) {
	script := &scriptedServer{replies: []string{
		toolFrame(0, "c1", "create_arrow", `{"from_id":"shape:t2","to_id":"shape:t2"}`) +
			finishFrame("tool_calls"),
		textFrame("ok") + finishFrame("stop"),
	}}
	exec := &fakeExecutor{fallback: agent.ToolOutcome{OK: true}}
	s, done := newToolSession(t, script, exec)
	defer done()

	s.SendTurn(context.Background(), "connect", nil)
	drain(t, s)

	if n := len(exec.invocations()); n != 0 {
		t.Errorf("executor ran %d times; a self-loop must never reach the canvas", n)
	}
	var told bool
	for _, m := range messagesOf(t, script.body(1)) {
		if m["role"] == "tool" && strings.Contains(m["content"].(string), "same shape") {
			told = true
		}
	}
	if !told {
		t.Error("the model should be told why the arrow was refused")
	}
}

func TestDistinctEndpointsStillPass(t *testing.T) {
	script := &scriptedServer{replies: []string{
		toolFrame(0, "c1", "create_arrow", `{"from_id":"shape:a","to_id":"shape:b"}`) +
			finishFrame("tool_calls"),
		textFrame("ok") + finishFrame("stop"),
	}}
	exec := &fakeExecutor{fallback: agent.ToolOutcome{OK: true}}
	s, done := newToolSession(t, script, exec)
	defer done()

	s.SendTurn(context.Background(), "connect", nil)
	drain(t, s)

	if n := len(exec.invocations()); n != 1 {
		t.Errorf("executor ran %d times, want 1", n)
	}
}

// Claiming a change that never happened is worse than failing: the person has to
// notice the canvas did not move. The loop nudges once, then says so plainly.
func TestClaimedEditWithNoToolCallIsChallengedThenReported(t *testing.T) {
	script := &scriptedServer{replies: []string{
		textFrame("I've added a decision diamond labelled 'Is it green tea?'") + finishFrame("stop"),
		textFrame("Added the diamond now.") + finishFrame("stop"),
	}}
	s, done := newToolSession(t, script, &fakeExecutor{})
	defer done()

	s.SendTurn(context.Background(), "add an if condition", nil)
	d := drain(t, s)

	if script.count() < 2 {
		t.Fatalf("model was asked %d times; it should be nudged once", script.count())
	}
	// The nudge must be visible to the model as a user turn.
	var nudged bool
	for _, m := range messagesOf(t, script.body(1)) {
		if m["role"] == "user" && strings.Contains(m["content"].(string), "did not call any") {
			nudged = true
		}
	}
	if !nudged {
		t.Error("the model should be told its claim had no tool call behind it")
	}
	var reported bool
	for _, e := range d.errors {
		if strings.Contains(e, "never made") {
			reported = true
		}
	}
	if !reported {
		t.Errorf("the user must be told the canvas is unchanged; errors = %v", d.errors)
	}
}

// A plain answer must not trip the claim detector.
func TestPlainAnswerIsNotChallenged(t *testing.T) {
	script := &scriptedServer{replies: []string{
		textFrame("The canvas shows a seven-step tea flow. Nothing is missing.") + finishFrame("stop"),
	}}
	s, done := newToolSession(t, script, &fakeExecutor{})
	defer done()

	s.SendTurn(context.Background(), "what is on my canvas?", nil)
	d := drain(t, s)

	if script.count() != 1 {
		t.Errorf("model was asked %d times; a plain answer needs no nudge", script.count())
	}
	if len(d.errors) != 0 {
		t.Errorf("unexpected errors on a plain answer: %v", d.errors)
	}
}

// Suggesting a change ("I can add…") is not claiming one.
func TestSuggestionIsNotAClaim(t *testing.T) {
	for _, s := range []string{
		"I can add a decision diamond if you like.",
		"You could connect the two boxes.",
		"Adding a diamond would make the branch clearer.",
	} {
		if claimsAnEdit(s) {
			t.Errorf("false positive on a suggestion: %q", s)
		}
	}
}

func TestPastTenseClaimsAreDetected(t *testing.T) {
	for _, s := range []string{
		"I've added a diamond.",
		"Added the decision node after Boil water.",
		"I connected the two branches.",
		"I have removed the old box.",
	} {
		if !claimsAnEdit(s) {
			t.Errorf("missed a claim: %q", s)
		}
	}
}
