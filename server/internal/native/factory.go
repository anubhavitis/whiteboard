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

// NewFactory builds a Factory with defaults filled in.
func NewFactory(baseURL, model, prompt string, log *slog.Logger) *Factory {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if model == "" {
		model = "local"
	}
	return &Factory{BaseURL: baseURL, Model: model, Prompt: prompt, Log: log, Timeout: 5 * time.Minute}
}

// Name is the value the chat panel's dropdown sends.
func (f *Factory) Name() string { return "local" }

// New starts a session. The executor is accepted and ignored: this agent is
// chat-only until spike S3 says the local model can be trusted with tools
// (planv2.md §0.7).
func (f *Factory) New(_ context.Context, _ string, _ agent.ToolExecutor) (agent.AgentSession, error) {
	timeout := f.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	return &Session{
		client:  &http.Client{Timeout: timeout},
		baseURL: f.BaseURL,
		model:   f.Model,
		prompt:  f.Prompt,
		log:     f.Log,
		events:  make(chan agent.AgentEvent, 64),
		closed:  make(chan struct{}),
	}, nil
}
