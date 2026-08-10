package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/anubhavitis/whiteboard/server/internal/agent"
)

// Session drives one long-lived `claude -p` subprocess.
//
// S1 established the shape of this: one process serves many turns with memory
// intact, and the prompt prefix is cached after the first turn (5,635 →
// 25 cache-creation tokens), so a persistent pipe is markedly cheaper than
// spawning per turn.
type Session struct {
	id     string
	log    *slog.Logger
	mcpURL string

	cmd     *exec.Cmd
	stdin   io.WriteCloser
	workdir string

	events chan agent.AgentEvent

	closeOnce sync.Once
	closed    chan struct{}

	// sessionID is Claude Code's own id, captured from the init event and used
	// to --resume after a crash (planv2.md §5.1).
	mu              sync.Mutex
	claudeSessionID string
}

// Config carries what a session needs to start.
type Config struct {
	// SessionID is the whiteboard's id; it becomes the MCP routing key.
	SessionID string
	// MCPBaseURL is where our MCP server listens, e.g. http://127.0.0.1:8787.
	MCPBaseURL string
	// SystemPrompt is appended to Claude Code's own prompt.
	SystemPrompt string
	// MaxTurns bounds one agent turn. Claude Code may spend a turn on
	// ToolSearch before the real tool, so leave headroom (S2 finding).
	MaxTurns int
	Log      *slog.Logger
}

// New starts the subprocess and begins pumping its events.
func New(ctx context.Context, cfg Config) (*Session, error) {
	workdir, err := os.MkdirTemp("", "whiteboard-"+cfg.SessionID+"-")
	if err != nil {
		return nil, fmt.Errorf("workdir: %w", err)
	}

	// An empty working directory is part of the isolation: no CLAUDE.md and no
	// SessionStart hook get discovered here. With the flags below this cut
	// per-turn cost by 83% in S1.
	mcpURL := cfg.MCPBaseURL + "/mcp/" + cfg.SessionID
	cfgPath, err := writeMCPConfig(workdir, mcpURL)
	if err != nil {
		os.RemoveAll(workdir)
		return nil, err
	}

	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 20
	}

	allowed := make([]string, 0, len(agent.Tools())+1)
	for _, t := range agent.Tools() {
		allowed = append(allowed, "mcp__canvas__"+t.Name)
	}
	// Claude Code may load a tool's schema via ToolSearch before calling it.
	allowed = append(allowed, "ToolSearch")

	cmd := exec.Command("claude",
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--strict-mcp-config",
		"--setting-sources", "",
		"--mcp-config", cfgPath,
		"--allowedTools", strings.Join(allowed, ","),
		// MCP tools are not "edits": acceptEdits never grants them and the
		// process hangs silently. This was S2's hour-long bug.
		"--permission-mode", "bypassPermissions",
		"--append-system-prompt", cfg.SystemPrompt,
		"--max-turns", fmt.Sprint(maxTurns),
	)
	cmd.Dir = workdir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		os.RemoveAll(workdir)
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		os.RemoveAll(workdir)
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		os.RemoveAll(workdir)
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		os.RemoveAll(workdir)
		return nil, fmt.Errorf("start claude: %w", err)
	}

	s := &Session{
		id:      cfg.SessionID,
		log:     cfg.Log,
		mcpURL:  mcpURL,
		cmd:     cmd,
		stdin:   stdin,
		workdir: workdir,
		events:  make(chan agent.AgentEvent, 64),
		closed:  make(chan struct{}),
	}

	go s.pump(stdout)
	go s.drainStderr(stderr)

	return s, nil
}

func writeMCPConfig(workdir, mcpURL string) (string, error) {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"canvas": map[string]any{"type": "http", "url": mcpURL},
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	path := workdir + "/mcp.json"
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// SendTurn queues one user message. It returns as soon as the message is
// written; progress arrives on Events.
func (s *Session) SendTurn(_ context.Context, text string, canvas json.RawMessage) error {
	content := text
	if len(canvas) > 0 {
		content = "<canvas>\n" + string(canvas) + "\n</canvas>\n\n" + text
	}

	msg, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]string{{"type": "text", "text": content}},
		},
	})
	if err != nil {
		return err
	}

	if _, err := s.stdin.Write(append(msg, '\n')); err != nil {
		return fmt.Errorf("write turn: %w", err)
	}
	return nil
}

func (s *Session) Events() <-chan agent.AgentEvent { return s.events }

// ClaudeSessionID is Claude Code's own session id, for --resume (planv2 §5.1)
// and for persistence (§3.3). Empty until the init event arrives.
func (s *Session) ClaudeSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claudeSessionID
}

func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.stdin.Close()
		if s.cmd.Process != nil {
			s.cmd.Process.Kill()
		}
		s.cmd.Wait()
		os.RemoveAll(s.workdir)
	})
	return nil
}

// emit posts an event unless the session is closing, so a dead reader can't
// wedge the pump.
func (s *Session) emit(ev agent.AgentEvent) {
	select {
	case s.events <- ev:
	case <-s.closed:
	}
}

// pump translates Claude Code's stream-json into AgentEvents.
func (s *Session) pump(stdout io.Reader) {
	defer close(s.events)

	scanner := bufio.NewScanner(stdout)
	// Canvas payloads and model output both run large; the 64KB default is not
	// enough.
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)

	for scanner.Scan() {
		var ev streamEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		s.handle(ev, scanner.Bytes())
	}

	if err := scanner.Err(); err != nil {
		select {
		case <-s.closed:
		default:
			s.log.Error("claude stream ended", "err", err, "session", s.id)
			s.emit(agent.AgentEvent{Type: agent.EventError, Text: "agent stream ended: " + err.Error()})
		}
	}
}

type streamEvent struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	Message   json.RawMessage `json:"message"`
	Result    string          `json:"result"`
	IsError   bool            `json:"is_error"`
}

func (s *Session) handle(ev streamEvent, raw []byte) {
	if ev.SessionID != "" {
		s.mu.Lock()
		s.claudeSessionID = ev.SessionID
		s.mu.Unlock()
	}

	switch ev.Type {
	case "assistant":
		s.handleAssistant(ev.Message)

	case "result":
		if ev.IsError {
			msg := ev.Result
			if msg == "" {
				msg = "agent turn failed"
			}
			s.emit(agent.AgentEvent{Type: agent.EventError, Text: msg})
		}
		s.emit(agent.AgentEvent{Type: agent.EventTurnDone})

	case "rate_limit_event":
		// Worth surfacing: the subscription window is shared with real work
		// (DECISIONS.md D6).
		var rl struct {
			Info struct {
				Status string `json:"status"`
			} `json:"rate_limit_info"`
		}
		if err := json.Unmarshal(raw, &rl); err == nil && rl.Info.Status != "allowed" {
			s.log.Warn("rate limit", "status", rl.Info.Status, "session", s.id)
			s.emit(agent.AgentEvent{
				Type: agent.EventError,
				Text: "Claude usage limit reached (" + rl.Info.Status + ").",
			})
		}
	}
}

func (s *Session) handleAssistant(raw json.RawMessage) {
	var msg struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}

	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				s.emit(agent.AgentEvent{Type: agent.EventTextDelta, Text: block.Text})
			}
		case "tool_use":
			// Informational only: the call itself arrives over MCP, which is
			// what actually executes it. This drives UI feedback.
			if strings.HasPrefix(block.Name, "mcp__canvas__") {
				s.emit(agent.AgentEvent{
					Type:     agent.EventToolCall,
					ToolID:   block.ID,
					ToolName: strings.TrimPrefix(block.Name, "mcp__canvas__"),
					ToolArgs: block.Input,
				})
			}
		}
	}
}

func (s *Session) drainStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			s.log.Debug("claude stderr", "session", s.id, "line", line)
		}
	}
}
