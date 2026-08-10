// Package claudecode drives Claude Code as an agent: a long-lived `claude -p`
// subprocess per session, plus the MCP server its tool calls come back through.
//
// This package and its tests are the ONLY place allowed to know about MCP,
// subprocesses, or the claude CLI (planv2.md import rule). Everything else
// talks to agent.AgentSession.
//
// The MCP implementation is stdlib JSON-RPC 2.0 rather than a library — see D7.
// Only three methods matter: initialize, tools/list, tools/call.
package claudecode

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/anubhavitis/whiteboard/server/internal/agent"
)

// protocolVersion is what we advertise. A client announcing a newer version
// (observed: 2025-11-25) accepts this without complaint.
const protocolVersion = "2024-11-05"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCPServer exposes the canvas tools over HTTP at /mcp/{sessionID}.
//
// The session id in the path is the routing key: it is how a tool call from one
// subprocess finds the one browser session allowed to execute it. S2 proved two
// concurrent sessions stay isolated this way.
type MCPServer struct {
	log *slog.Logger

	mu        sync.RWMutex
	executors map[string]agent.ToolExecutor
}

func NewMCPServer(log *slog.Logger) *MCPServer {
	return &MCPServer{log: log, executors: make(map[string]agent.ToolExecutor)}
}

// Register binds a session id to the executor that serves its tool calls.
func (s *MCPServer) Register(sessionID string, exec agent.ToolExecutor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executors[sessionID] = exec
}

// Unregister drops a session. Later calls for it return an error to the agent
// rather than hanging.
func (s *MCPServer) Unregister(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.executors, sessionID)
}

func (s *MCPServer) executorFor(sessionID string) (agent.ToolExecutor, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	exec, ok := s.executors[sessionID]
	return exec, ok
}

func (s *MCPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimPrefix(r.URL.Path, "/mcp/")
	if sessionID == "" || sessionID == r.URL.Path {
		http.Error(w, "missing session id in path", http.StatusBadRequest)
		return
	}

	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Notifications (e.g. notifications/initialized) arrive with an empty
		// body. Treat an undecodable body as one rather than erroring.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Notifications carry no id and expect no response.
	if strings.HasPrefix(req.Method, "notifications/") {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}

	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "canvas", "version": "0.1.0"},
		}

	case "tools/list":
		resp.Result = map[string]any{"tools": agent.MCPTools()}

	case "tools/call":
		resp.Result, resp.Error = s.callTool(r.Context(), sessionID, req.Params)

	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.log.Error("mcp write failed", "err", err, "session", sessionID)
	}
}

// callTool routes one tool call to its session's executor and blocks for the
// result — which, per D5, means a round trip to the browser.
func (s *MCPServer) callTool(ctx context.Context, sessionID string, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      struct {
			ToolUseID string `json:"claudecode/toolUseId"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "bad params: " + err.Error()}
	}

	exec, ok := s.executorFor(sessionID)
	if !ok {
		// The session ended while the agent was still thinking.
		return nil, &rpcError{Code: -32603, Message: "no active canvas session: " + sessionID}
	}

	id := p.Meta.ToolUseID
	if id == "" {
		id = p.Name
	}

	s.log.Info("mcp tool call", "session", sessionID, "tool", p.Name)

	outcome := exec.Execute(ctx, agent.ToolInvocation{
		ID:   id,
		Name: p.Name,
		Args: p.Arguments,
	})

	// Tool failures are reported to the model as content with isError, not as
	// a JSON-RPC error: the model should see them and adapt, not treat the
	// transport as broken.
	if !outcome.OK {
		return map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": outcome.Error}},
		}, nil
	}

	result, err := json.Marshal(map[string]any{
		"ok":                  true,
		"resulting_shape_ids": outcome.ShapeIDs,
	})
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(result)}},
	}, nil
}
