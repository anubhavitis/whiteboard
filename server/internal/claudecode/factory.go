package claudecode

import (
	"context"
	"log/slog"

	"github.com/anubhavitis/whiteboard/server/internal/agent"
)

// Factory creates Claude Code sessions and registers each one with the MCP
// server so its tool calls can be routed home.
type Factory struct {
	mcp     *MCPServer
	baseURL string
	prompt  string
	log     *slog.Logger
}

func NewFactory(mcp *MCPServer, baseURL, systemPrompt string, log *slog.Logger) *Factory {
	return &Factory{mcp: mcp, baseURL: baseURL, prompt: systemPrompt, log: log}
}

func (f *Factory) Name() string { return "claude-code" }

// WithPrompt returns a copy of this factory that appends the given system prompt,
// so a per-session skill selection cannot leak between sessions.
func (f *Factory) WithPrompt(prompt string) agent.Factory {
	copy := *f
	copy.prompt = prompt
	return &copy
}

func (f *Factory) New(ctx context.Context, sessionID string, exec agent.ToolExecutor) (agent.AgentSession, error) {
	// Register before starting the subprocess: Claude Code connects to the MCP
	// endpoint during init, and an unregistered session would reject the first
	// tool call.
	f.mcp.Register(sessionID, exec)

	sess, err := New(ctx, Config{
		SessionID:    sessionID,
		MCPBaseURL:   f.baseURL,
		SystemPrompt: f.prompt,
		Log:          f.log,
	})
	if err != nil {
		f.mcp.Unregister(sessionID)
		return nil, err
	}

	return &registeredSession{Session: sess, mcp: f.mcp, id: sessionID}, nil
}

// registeredSession unregisters from the MCP server on close, so a finished
// session's late tool calls fail fast instead of hanging.
type registeredSession struct {
	*Session
	mcp *MCPServer
	id  string
}

func (r *registeredSession) Close() error {
	r.mcp.Unregister(r.id)
	return r.Session.Close()
}
