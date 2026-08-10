// SPIKE S2 — MCP callback with per-session routing (planv2.md §0.6).
//
// Proves the piece that is OUR design rather than a documented feature: one Go
// MCP server, a per-session URL path, and two concurrent claude subprocesses
// whose tool calls each come back tagged with the right session.
//
// MCP over HTTP is JSON-RPC 2.0. Implementing the three methods we need
// (initialize / tools/list / tools/call) against stdlib avoids a dependency —
// and, in production, keeps the "only the agent package knows about MCP" rule
// cheap to honour.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const protocolVersion = "2024-11-05"

// --- JSON-RPC plumbing ---

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

// toolCall is what the server observed, tagged with the session that made it.
type toolCall struct {
	SessionID string
	Tool      string
	Args      map[string]any
}

// mcpServer routes each request by the {sessionID} in its URL path.
type mcpServer struct {
	mu       sync.Mutex
	observed []toolCall
}

func (s *mcpServer) record(c toolCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observed = append(s.observed, c)
}

func (s *mcpServer) calls() []toolCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]toolCall, len(s.observed))
	copy(out, s.observed)
	return out
}

func (s *mcpServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// /mcp/{sessionID} — the routing key. In production this is how a tool
	// call finds the browser session that must execute it.
	sessionID := strings.TrimPrefix(r.URL.Path, "/mcp/")
	if sessionID == "" || sessionID == r.URL.Path {
		http.Error(w, "missing session id in path", http.StatusBadRequest)
		return
	}

	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Notifications legitimately arrive with an empty body.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}

	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "canvas", "version": "0.0.1"},
		}

	case "notifications/initialized":
		// Notification: no id, no response body expected.
		w.WriteHeader(http.StatusAccepted)
		return

	case "tools/list":
		resp.Result = map[string]any{
			"tools": []map[string]any{{
				"name":        "create_shape",
				"description": "Create a labelled shape on the whiteboard canvas.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"text": map[string]any{"type": "string", "description": "Label for the shape."},
					},
					"required": []string{"text"},
				},
			}},
		}

	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: "bad params"}
			break
		}
		s.record(toolCall{SessionID: sessionID, Tool: params.Name, Args: params.Arguments})

		// In production this blocks on the browser round-trip. Here we answer
		// immediately with a plausible result.
		resp.Result = map[string]any{
			"content": []map[string]any{{
				"type": "text",
				"text": fmt.Sprintf(`{"ok":true,"resulting_shape_ids":["shape:%s-1"]}`, sessionID),
			}},
		}

	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// --- driving claude ---

// runSession spawns one isolated claude subprocess wired to this session's own
// MCP endpoint, sends one turn, and returns the assistant text.
func runSession(baseURL, sessionID, prompt string) (string, error) {
	workdir, err := os.MkdirTemp("", "s2-"+sessionID+"-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(workdir)

	// Per-session mcp-config: the URL carries the routing key.
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"canvas": map[string]any{
				"type": "http",
				"url":  baseURL + "/mcp/" + sessionID,
			},
		},
	}
	cfgPath := workdir + "/mcp.json"
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		return "", err
	}

	cmd := exec.Command("claude",
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--strict-mcp-config",
		"--setting-sources", "",
		"--mcp-config", cfgPath,
		"--allowedTools", "mcp__canvas__create_shape,ToolSearch",
		"--permission-mode", "bypassPermissions",
		"--append-system-prompt",
			"You draw on a whiteboard. When asked to add something, call the create_shape tool. Do not ask for confirmation.",
		"--max-turns", "8",
	)
	cmd.Dir = workdir
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	defer func() {
		stdin.Close()
		cmd.Wait()
	}()

	msg, _ := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]string{{"type": "text", "text": prompt}},
		},
	})
	if _, err := stdin.Write(append(msg, '\n')); err != nil {
		return "", err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)

	var text strings.Builder
	for scanner.Scan() {
		var ev struct {
			Type    string          `json:"type"`
			Subtype string          `json:"subtype"`
			Message json.RawMessage `json:"message"`
			Result  string          `json:"result"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "system":
			if ev.Subtype == "init" {
				var init struct {
					MCPServers []struct {
						Name   string `json:"name"`
						Status string `json:"status"`
					} `json:"mcp_servers"`
					Tools []string `json:"tools"`
				}
				if err := json.Unmarshal(scanner.Bytes(), &init); err == nil {
					for _, srv := range init.MCPServers {
						fmt.Printf("  [%s] mcp server %q status=%s\n", sessionID, srv.Name, srv.Status)
					}
					for _, t := range init.Tools {
						if strings.HasPrefix(t, "mcp__canvas") {
							fmt.Printf("  [%s] tool discovered: %s\n", sessionID, t)
						}
					}
				}
			}
		case "result":
			text.WriteString(ev.Result)
			return text.String(), nil
		}
	}
	return text.String(), scanner.Err()
}

func main() {
	srv := &mcpServer{}
	httpSrv := httptest.NewServer(srv)
	defer httpSrv.Close()

	fmt.Println("=== S2: MCP callback with per-session routing ===")
	fmt.Printf("mcp endpoint: %s/mcp/{sessionID}\n\n", httpSrv.URL)

	// Two concurrent sessions — the real question. Each must be routed back
	// to its own path, with no cross-talk.
	var wg sync.WaitGroup
	results := make(map[string]string)
	var mu sync.Mutex

	for _, s := range []struct{ id, prompt string }{
		{"alpha", "Add a shape labelled 'Alpha Service' to the canvas."},
		{"beta", "Add a shape labelled 'Beta Service' to the canvas."},
	} {
		wg.Add(1)
		go func(id, prompt string) {
			defer wg.Done()
			out, err := runSession(httpSrv.URL, id, prompt)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				results[id] = "ERROR: " + err.Error()
				return
			}
			results[id] = out
		}(s.id, s.prompt)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Minute):
		fmt.Println("TIMEOUT waiting for sessions")
	}

	fmt.Println("\n--- observed tool calls ---")
	calls := srv.calls()
	for _, c := range calls {
		fmt.Printf("  session=%-6s tool=%-14s args=%v\n", c.SessionID, c.Tool, c.Args)
	}

	fmt.Println("\n--- routing check ---")
	bySession := map[string][]toolCall{}
	for _, c := range calls {
		bySession[c.SessionID] = append(bySession[c.SessionID], c)
	}

	routedCorrectly := true
	for id, want := range map[string]string{"alpha": "Alpha", "beta": "Beta"} {
		got := bySession[id]
		if len(got) == 0 {
			fmt.Printf("  %s: NO CALLS\n", id)
			routedCorrectly = false
			continue
		}
		label, _ := got[0].Args["text"].(string)
		ok := strings.Contains(label, want)
		fmt.Printf("  %s: %d call(s), label=%q correct=%v\n", id, len(got), label, ok)
		if !ok {
			routedCorrectly = false
		}
	}

	fmt.Println("\n=== VERDICT ===")
	fmt.Printf("claude discovered MCP server : %v\n", len(calls) > 0)
	fmt.Printf("tool call round-tripped      : %v\n", len(calls) > 0)
	fmt.Printf("per-session routing correct  : %v\n", routedCorrectly)
	fmt.Printf("concurrent sessions isolated : %v\n", len(bySession) == 2 && routedCorrectly)
}
