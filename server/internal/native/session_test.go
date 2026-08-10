package native

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anubhavitis/whiteboard/server/internal/agent"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// sseServer streams the given content chunks as OpenAI-style SSE frames.
func sseServer(t *testing.T, chunks []string, capture *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			b, _ := io.ReadAll(r.Body)
			*capture = b
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("recorder is not a flusher")
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", c)
			fl.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
}

// collect drains events until turn_done or timeout.
func collect(t *testing.T, s agent.AgentSession) (text string, sawError bool) {
	t.Helper()
	var b strings.Builder
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			switch ev.Type {
			case agent.EventTextDelta:
				b.WriteString(ev.Text)
			case agent.EventError:
				sawError = true
			case agent.EventTurnDone:
				return b.String(), sawError
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn_done")
		}
	}
}

func TestStreamsDeltasAndAssemblesReply(t *testing.T) {
	srv := sseServer(t, []string{"Hello", " ", "world"}, nil)
	defer srv.Close()

	f := NewFactory(srv.URL+"/v1", "test-model", "SYS", quietLog())
	s, err := f.New(context.Background(), "sess", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.SendTurn(context.Background(), "hi", json.RawMessage(`{"shapes":[]}`)); err != nil {
		t.Fatal(err)
	}
	got, sawErr := collect(t, s)
	if sawErr {
		t.Error("unexpected error event")
	}
	if got != "Hello world" {
		t.Errorf("text = %q, want %q", got, "Hello world")
	}
}

func TestSendsSystemPromptAndCanvas(t *testing.T) {
	var body []byte
	srv := sseServer(t, []string{"ok"}, &body)
	defer srv.Close()

	f := NewFactory(srv.URL+"/v1", "test-model", "SYSTEM-PROMPT-MARKER", quietLog())
	s, _ := f.New(context.Background(), "sess", nil)
	defer s.Close()

	canvas := json.RawMessage(`{"shapes":[{"id":"shape:a","text":"API"}]}`)
	if err := s.SendTurn(context.Background(), "what is this", canvas); err != nil {
		t.Fatal(err)
	}
	collect(t, s)

	var req struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("request body not JSON: %v\n%s", err, body)
	}
	if !req.Stream {
		t.Error("stream should be true")
	}
	if req.Model != "test-model" {
		t.Errorf("model = %q", req.Model)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("want system+user, got %d messages", len(req.Messages))
	}
	if req.Messages[0].Role != "system" || req.Messages[0].Content != "SYSTEM-PROMPT-MARKER" {
		t.Errorf("system message = %+v", req.Messages[0])
	}
	if !strings.Contains(req.Messages[1].Content, `"shape:a"`) {
		t.Error("canvas JSON missing from the user turn")
	}
	if !strings.Contains(req.Messages[1].Content, "what is this") {
		t.Error("user text missing from the user turn")
	}
}

// Multi-turn memory is this session's own job: the endpoint is stateless.
func TestKeepsHistoryAcrossTurns(t *testing.T) {
	var body []byte
	srv := sseServer(t, []string{"first-reply"}, &body)
	defer srv.Close()

	f := NewFactory(srv.URL+"/v1", "m", "SYS", quietLog())
	s, _ := f.New(context.Background(), "sess", nil)
	defer s.Close()

	if err := s.SendTurn(context.Background(), "turn one", nil); err != nil {
		t.Fatal(err)
	}
	collect(t, s)

	if err := s.SendTurn(context.Background(), "turn two", nil); err != nil {
		t.Fatal(err)
	}
	collect(t, s)

	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	// system + user(1) + assistant(1) + user(2)
	if len(req.Messages) != 4 {
		t.Fatalf("want 4 messages on turn two, got %d: %+v", len(req.Messages), req.Messages)
	}
	if req.Messages[2].Role != "assistant" || req.Messages[2].Content != "first-reply" {
		t.Errorf("assistant turn not replayed: %+v", req.Messages[2])
	}
	if !strings.Contains(req.Messages[3].Content, "turn two") {
		t.Errorf("second user turn missing: %+v", req.Messages[3])
	}
}

func TestHTTPErrorSurfacesAsEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not loaded", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	f := NewFactory(srv.URL+"/v1", "m", "SYS", quietLog())
	s, _ := f.New(context.Background(), "sess", nil)
	defer s.Close()

	if err := s.SendTurn(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	_, sawErr := collect(t, s)
	if !sawErr {
		t.Error("a 503 must surface as an error event, never be swallowed")
	}
}

// Errors must not be silently swallowed, but a cancelled turn is the Stop
// button and should end quietly.
func TestCancelledTurnEndsWithoutError(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // hold the turn open until the test cancels it
	}))
	defer srv.Close()
	defer close(block)

	f := NewFactory(srv.URL+"/v1", "m", "SYS", quietLog())
	s, _ := f.New(context.Background(), "sess", nil)
	defer s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	if err := s.SendTurn(ctx, "hi", nil); err != nil {
		t.Fatal(err)
	}
	cancel()

	_, sawErr := collect(t, s)
	if sawErr {
		t.Error("cancellation should not report an error to the user")
	}
}

func TestRejectsInvalidCanvasJSON(t *testing.T) {
	f := NewFactory("http://127.0.0.1:1", "m", "SYS", quietLog())
	s, _ := f.New(context.Background(), "sess", nil)
	defer s.Close()

	if err := s.SendTurn(context.Background(), "hi", json.RawMessage(`{broken`)); err == nil {
		t.Error("want an error for malformed canvas JSON")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	f := NewFactory("http://127.0.0.1:1", "m", "SYS", quietLog())
	s, _ := f.New(context.Background(), "sess", nil)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close must be safe: %v", err)
	}
}

// A malformed SSE frame mid-stream should not lose the surrounding text.
func TestSkipsUnparseableChunks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"A\"}}]}\n\n")
		fmt.Fprint(w, "data: {not json\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"B\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	f := NewFactory(srv.URL+"/v1", "m", "SYS", quietLog())
	s, _ := f.New(context.Background(), "sess", nil)
	defer s.Close()

	if err := s.SendTurn(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	got, _ := collect(t, s)
	if got != "AB" {
		t.Errorf("text = %q, want %q", got, "AB")
	}
}

func TestFactoryNameIsStableForTheDropdown(t *testing.T) {
	if got := NewFactory("", "", "", quietLog()).Name(); got != "local" {
		t.Errorf("Name() = %q, want %q", got, "local")
	}
}
