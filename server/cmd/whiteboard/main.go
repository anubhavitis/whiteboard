package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/anubhavitis/whiteboard/server/internal/agent"
	"github.com/anubhavitis/whiteboard/server/internal/claudecode"
	"github.com/anubhavitis/whiteboard/server/internal/httpapi"
	"github.com/anubhavitis/whiteboard/server/internal/native"
	"github.com/anubhavitis/whiteboard/server/internal/ws"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	addr := env("WHITEBOARD_ADDR", ":8787")
	origins := strings.Split(env("WHITEBOARD_ALLOWED_ORIGINS", "localhost:5173"), ",")

	// Claude Code reaches our MCP server over loopback. The bind address may be
	// ":8787", which is not dialable, so build the URL from the port.
	mcpBase := env("WHITEBOARD_MCP_BASE_URL", "http://127.0.0.1"+portOf(addr))

	mcp := claudecode.NewMCPServer(log)

	// Every agent that can actually run, in dropdown order. WHITEBOARD_AGENT
	// still picks the default; the browser switches among the rest per session
	// (planv2.md §1.2).
	factories := availableAgents(env("WHITEBOARD_AGENT", ""), mcp, mcpBase, log)

	// User skills live outside the repository on purpose: they are one person's
	// notes on how they want their agent to behave, not project source. The
	// directory is gitignored and created on first save.
	skillDir := env("WHITEBOARD_SKILLS_DIR", "skills")
	skills, err := agent.NewSkillStore(skillDir)
	if err != nil {
		log.Error("could not load skills", "err", err, "dir", skillDir)
		os.Exit(1)
	}
	log.Info("skills loaded", "dir", skillDir, "count", len(skills.List()))

	handler := ws.NewHandlerWithAgents(factories, log, origins).WithSkills(skills)
	srv := &http.Server{
		Addr:              addr,
		Handler:           httpapi.Router(handler, mcp),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: sessions are long-lived WebSockets, and an MCP tool
		// call blocks until the browser answers.
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("listening", "addr", addr, "origins", origins,
			"default_agent", factories[0].Name(), "agents", agentNames(factories))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown failed", "err", err)
	}
}

// availableAgents lists every agent this process can actually run, with the one
// named by WHITEBOARD_AGENT first so it becomes the session default.
//
// Only agents that can really start are offered: a dropdown entry that always
// errors is worse than no entry. Claude Code therefore appears only when the
// CLI is on PATH.
func availableAgents(choice string, mcp *claudecode.MCPServer, mcpBase string, log *slog.Logger) []agent.Factory {
	var claudeF agent.Factory
	if path, err := exec.LookPath("claude"); err == nil {
		log.Info("claude CLI found", "path", path, "mcp_base", mcpBase)
		claudeF = claudecode.NewFactory(mcp, mcpBase, agent.SystemPrompt, log)
	} else {
		log.Warn("claude CLI not on PATH — the claude agent will not be offered")
	}

	localF := native.NewFactory(
		env("WHITEBOARD_LOCAL_BASE_URL", native.DefaultBaseURL),
		env("WHITEBOARD_LOCAL_MODEL", native.DefaultModel),
		agent.SystemPrompt, log)
	log.Info("local agent configured", "base_url", localF.BaseURL, "model", localF.Model,
		"tools", "enabled")

	// The local agent is always offered even when mlx_lm.server is down: it is a
	// per-turn HTTP call, so it starts fine and reports a clear connection error
	// rather than failing at session start.
	all := []agent.Factory{}
	if claudeF != nil {
		all = append(all, claudeF)
	}
	all = append(all, localF, agent.EchoFactory{})

	// Move the requested default to the front.
	want := map[string]string{
		"claude": "claude-code", "claudecode": "claude-code", "claude-code": "claude-code",
		"local": "local", "native": "local", "echo": "echo",
	}[choice]
	if choice != "" && want == "" {
		log.Error("unknown WHITEBOARD_AGENT, using the first available", "value", choice)
	}
	if want != "" {
		for i, f := range all {
			if f.Name() == want {
				all[0], all[i] = all[i], all[0]
				return all
			}
		}
		log.Error("WHITEBOARD_AGENT is not available, using the first one",
			"wanted", choice, "available", agentNames(all))
	}
	return all
}

func agentNames(fs []agent.Factory) []string {
	names := make([]string, 0, len(fs))
	for _, f := range fs {
		names = append(names, f.Name())
	}
	return names
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// portOf turns a bind address into a dialable ":port" suffix.
func portOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ":8787"
}
