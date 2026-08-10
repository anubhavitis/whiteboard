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

	var factory agent.Factory
	if path, err := exec.LookPath("claude"); err == nil {
		log.Info("claude CLI found", "path", path, "mcp_base", mcpBase)
		factory = claudecode.NewFactory(mcp, mcpBase, agent.SystemPrompt, log)
	} else {
		log.Warn("claude CLI not found on PATH — falling back to echo agent")
		factory = agent.EchoFactory{}
	}

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
