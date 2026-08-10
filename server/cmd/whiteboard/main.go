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

	// WHITEBOARD_AGENT picks the agent explicitly; empty means autodetect.
	// A per-session dropdown is planv2.md §1.2 and needs a protocol field, so
	// this stays an env var until that lands.
	factory := pickAgent(env("WHITEBOARD_AGENT", ""), mcp, mcpBase, log)

	handler := ws.NewHandler(factory, log, origins)
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
		log.Info("listening", "addr", addr, "origins", origins, "agent", factory.Name())
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

// pickAgent resolves which agent to run. Named choices fail loudly rather than
// silently degrading: asking for "local" and getting echo would look like a
// broken model.
func pickAgent(choice string, mcp *claudecode.MCPServer, mcpBase string, log *slog.Logger) agent.Factory {
	localFactory := func() agent.Factory {
		f := native.NewFactory(
			env("WHITEBOARD_LOCAL_BASE_URL", native.DefaultBaseURL),
			env("WHITEBOARD_LOCAL_MODEL", "local"),
			agent.SystemPrompt, log)
		log.Info("using local agent", "base_url", f.BaseURL, "model", f.Model,
			"note", "chat-only until spike S3 (planv2.md 0.7) says otherwise")
		return f
	}

	switch choice {
	case "local", "native":
		return localFactory()
	case "echo":
		log.Info("using echo agent (explicitly requested)")
		return agent.EchoFactory{}
	case "claude", "claudecode":
		if path, err := exec.LookPath("claude"); err == nil {
			log.Info("claude CLI found", "path", path, "mcp_base", mcpBase)
			return claudecode.NewFactory(mcp, mcpBase, agent.SystemPrompt, log)
		}
		log.Error("WHITEBOARD_AGENT=claude but the claude CLI is not on PATH")
		return agent.EchoFactory{}
	case "":
		// Autodetect, unchanged: Claude Code is the working agent today.
		if path, err := exec.LookPath("claude"); err == nil {
			log.Info("claude CLI found", "path", path, "mcp_base", mcpBase)
			return claudecode.NewFactory(mcp, mcpBase, agent.SystemPrompt, log)
		}
		log.Warn("claude CLI not found on PATH — falling back to echo agent")
		return agent.EchoFactory{}
	default:
		log.Error("unknown WHITEBOARD_AGENT, falling back to echo", "value", choice)
		return agent.EchoFactory{}
	}
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
