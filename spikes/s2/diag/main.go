// S2 diagnostic: one session, every event printed, so we can see exactly where
// the tool call stalls.
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
	"time"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := os.ReadFile("/dev/null")
		_ = body
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fmt.Println("  >> MCP: undecodable body:", err)
			http.Error(w, "bad json", 400)
			return
		}
		fmt.Printf("  >> MCP %s path=%s params=%s\n", req.Method, r.URL.Path, truncate(string(req.Params), 120))

		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			resp["result"] = map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "canvas", "version": "0.0.1"},
			}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			resp["result"] = map[string]any{"tools": []map[string]any{{
				"name":        "create_shape",
				"description": "Create a labelled shape on the whiteboard canvas.",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
					"required":   []string{"text"},
				},
			}}}
		case "tools/call":
			fmt.Println("  >> MCP: TOOL CALL RECEIVED")
			resp["result"] = map[string]any{
				"content": []map[string]any{{"type": "text", "text": `{"ok":true,"resulting_shape_ids":["shape:x1"]}`}},
			}
		default:
			resp["error"] = map[string]any{"code": -32601, "message": "no method " + req.Method}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	workdir, _ := os.MkdirTemp("", "s2diag-")
	defer os.RemoveAll(workdir)

	cfg := map[string]any{"mcpServers": map[string]any{
		"canvas": map[string]any{"type": "http", "url": srv.URL + "/mcp/alpha"},
	}}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	cfgPath := workdir + "/mcp.json"
	os.WriteFile(cfgPath, data, 0o600)

	cmd := exec.Command("claude",
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--strict-mcp-config",
		"--setting-sources", "",
		"--mcp-config", cfgPath,
		"--allowedTools", "mcp__canvas__create_shape",
		"--permission-mode", "bypassPermissions",
		"--append-system-prompt", "You draw on a whiteboard. Call create_shape when asked to add something.",
		"--max-turns", "6",
	)
	cmd.Dir = workdir
	cmd.Stderr = os.Stderr

	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		fmt.Println("start:", err)
		return
	}

	msg, _ := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{"role": "user",
			"content": []map[string]string{{"type": "text", "text": "Add a shape labelled 'Alpha Service'."}}},
	})
	stdin.Write(append(msg, '\n'))

	go func() {
		time.Sleep(120 * time.Second)
		fmt.Println("\n!! 120s elapsed — killing")
		cmd.Process.Kill()
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		var ev map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		t, _ := ev["type"].(string)
		sub, _ := ev["subtype"].(string)

		switch t {
		case "assistant", "user":
			if m, ok := ev["message"].(map[string]any); ok {
				if content, ok := m["content"].([]any); ok {
					for _, c := range content {
						cm, _ := c.(map[string]any)
						ct, _ := cm["type"].(string)
						switch ct {
						case "text":
							fmt.Printf("[%s/text] %s\n", t, truncate(fmt.Sprint(cm["text"]), 100))
						case "tool_use":
							fmt.Printf("[%s/tool_use] name=%v input=%v\n", t, cm["name"], cm["input"])
						case "tool_result":
							fmt.Printf("[%s/tool_result] %s\n", t, truncate(fmt.Sprint(cm["content"]), 100))
						}
					}
				}
			}
		case "result":
			fmt.Printf("[result] subtype=%s is_error=%v result=%s\n",
				sub, ev["is_error"], truncate(fmt.Sprint(ev["result"]), 200))
			if pd, ok := ev["permission_denials"].([]any); ok && len(pd) > 0 {
				fmt.Printf("[result] PERMISSION DENIALS: %v\n", pd)
			}
		case "system":
			if sub != "init" {
				fmt.Printf("[system/%s]\n", sub)
			}
		default:
			fmt.Printf("[%s/%s]\n", t, sub)
		}
	}
	stdin.Close()
	cmd.Wait()
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
