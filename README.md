# Whiteboard Partner

A local-first AI thinking partner on an infinite canvas. Chat and canvas share one brain: the agent
can read what you've drawn and draw back.

**Stack:** React + tldraw · Go (session hub, MCP server) · SQLite · Claude Code subprocess (no API key) + MLX/Qwen3 later

## Status

Phases 0–2 of `planv2.md` complete and verified end to end. The agent reads the canvas and draws on
it: adds shapes, connects them with arrows, relabels, deletes. Agent-drawn shapes are violet so you
can tell who drew what.

**No API key needed.** The agent is a `claude -p` subprocess authenticated by your Claude Code
subscription; canvas tools reach it through an MCP server the Go backend hosts. If `claude` is not on
PATH the canvas still works and the agent echoes your text back.

A real turn, captured from the running server:

```
you    : "Add a Postgres database below the API Gateway and connect them."
agent  : create_shape {"shape":"box","text":"Postgres","near":"shape:api","direction":"below"}
         create_arrow {"from_id":"shape:api","to_id":"shape:gen1"}
         "…one thing worth noting: a gateway talking straight to a database is unusual."
```

Note it never emits coordinates — placement is relative, and the frontend computes pixels.

## Running it

```sh
make install
make dev
```

Frontend on http://localhost:5173, server on http://localhost:8787. The canvas fills the window with
the chat panel docked right; a dot in the panel header shows the agent connection — amber connecting,
green connected, red disconnected.

## Layout

| Path | What |
| --- | --- |
| `web/` | Vite + React + tldraw. Owns the canvas and executes agent tools. |
| `server/` | Go. Owns the model loop and the WebSocket session. |
| `plan.md` | The build plan: phases, exit criteria, risks, non-goals. |
| `planv2.md` | The current build plan (`plan.md` is the superseded v1). |
| `DECISIONS.md` | D1–D7, the decisions that are expensive to reverse. |
| `spikes/` | Feasibility spikes + `FINDINGS.md`; re-run after a `claude` upgrade. |

## Configuration

| Variable | Default | Where |
| --- | --- | --- |
| `WHITEBOARD_ADDR` | `:8787` | server |
| `WHITEBOARD_ALLOWED_ORIGINS` | `localhost:5173` | server, comma-separated |
| `WHITEBOARD_MCP_BASE_URL` | `http://127.0.0.1:<port>` | server — where subprocesses call tools back |
| `VITE_WS_URL` | `ws://localhost:8787/ws` | web |

Requires the `claude` CLI on PATH, logged in. No API key.

## Known limits

- **Turns take ~10–30s.** Claude Code is optimised for depth, not chat latency. Fine for "critique
  this architecture", sluggish for rapid back-and-forth. A local model (planv2 §4.2) is the fix and
  is not built yet.
- **Usage is shared with your normal Claude Code work** — same subscription window. Config isolation
  keeps a turn ~6x cheaper than the naive setup; see `spikes/FINDINGS.md`.
- **The `claude` CLI is a moving dependency.** After upgrading it, re-run the spikes in `spikes/`
  before trusting sessions (planv2 §5.5). Verified against 2.1.226.

## Licensing note

tldraw is not MIT. The free tier carries a "Made with tldraw" watermark; removing it requires a paid
license. Accepted for a personal tool — see D2 in `DECISIONS.md` if this ever ships as a product.
