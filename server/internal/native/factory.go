package native

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/anubhavitis/whiteboard/server/internal/agent"
)

// DefaultBaseURL is where `mlx_lm.server` listens by default.
const DefaultBaseURL = "http://127.0.0.1:8080/v1"

// Factory creates local-model sessions.
//
// Model and base URL are configuration, not code (planv2.md §4.1): swapping
// Qwen3 for another MLX model, or MLX for any OpenAI-compatible endpoint, must
// not require a code change here.
type Factory struct {
	BaseURL string
	Model   string
	Prompt  string
	Log     *slog.Logger

	// Timeout bounds one turn. Local prompt processing on a large canvas is
	// slow (planv2.md §0.7 measures it), so this is generous by design.
	Timeout time.Duration
}

// DefaultModel is the model mlx_lm.server is expected to be serving.
//
// This must be a real HuggingFace repo id, NOT a friendly alias: mlx_lm.server
// resolves whatever `model` it is sent against the Hub, so "local" produces a
// 404 with a "Repository Not Found for url: .../models/local" body rather than
// falling back to the loaded model. Verified against mlx_lm 0.31.3.
const DefaultModel = "mlx-community/Qwen3-30B-A3B-Instruct-2507-8bit"

// NewFactory builds a Factory with defaults filled in.
func NewFactory(baseURL, model, prompt string, log *slog.Logger) *Factory {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if model == "" {
		model = DefaultModel
	}
	return &Factory{BaseURL: baseURL, Model: model, Prompt: prompt, Log: log, Timeout: 5 * time.Minute}
}

// WithPrompt returns a copy of this factory that builds sessions with the given
// system prompt, so a per-session skill selection cannot leak between sessions.
func (f *Factory) WithPrompt(prompt string) agent.Factory {
	copy := *f
	copy.Prompt = prompt
	return &copy
}

// Name is the value the chat panel's dropdown sends.
func (f *Factory) Name() string { return "local" }

// New starts a session. A non-nil executor turns on the tool loop; nil keeps the
// session chat-only.
func (f *Factory) New(_ context.Context, _ string, exec agent.ToolExecutor) (agent.AgentSession, error) {
	timeout := f.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	return &Session{
		client: &client{
			http:    &http.Client{Timeout: timeout},
			baseURL: f.BaseURL,
			model:   f.Model,
			log:     f.Log,
		},
		prompt:   f.Prompt,
		log:      f.Log,
		executor: exec,
		events:   make(chan agent.AgentEvent, 64),
		closed:   make(chan struct{}),
	}, nil
}
