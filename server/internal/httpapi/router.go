// Package httpapi assembles the server's HTTP surface. It owns routing only;
// behaviour lives in the packages it mounts.
package httpapi

import (
	"encoding/json"
	"net/http"
)

// Router builds the mux.
//
//   - /ws   is the browser session (D4).
//   - /mcp/ is where Claude Code subprocesses call our canvas tools back; the
//     session id in the path is the routing key.
func Router(wsHandler, mcpHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.Handle("/ws", wsHandler)
	mux.Handle("/mcp/", mcpHandler)
	return mux
}

func healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
