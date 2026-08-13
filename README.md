# Whiteboard Partner

A local-first AI thinking partner on an infinite canvas. Chat and canvas share one brain: the agent
reads what you've drawn and draws back.

**Stack:** React + tldraw · Go (session hub, MCP server) · two interchangeable agents: a Claude Code
subprocess (no API key) and a local MLX/Qwen3 model. Canvas state lives in the browser's tldraw store.

<p align="center">
  <img src="docs/turn-flow.svg" alt="A turn flows from the browser to ws/handler, out to whichever agent is running, and back to ws/executor, which asks the browser to execute each canvas tool. Claude Code returns tool calls over MCP; the local MLX model has them parsed out of its stream." width="100%">
</p>

Either agent reads the canvas and draws on it: adds shapes, connects them with arrows, relabels,
deletes. Agent-drawn shapes are violet so you can tell who drew what. Grey above is the chat path,
violet the drawing path — and the browser, not the backend, applies every change, so undo/redo works
on the agent's edits exactly as it does on yours.

**No API key either way.** Claude Code runs as a `claude -p` subprocess authenticated by your
subscription, with canvas tools reaching it through an MCP server the Go backend hosts. The local
agent talks to `mlx_lm.server` on your own machine and costs nothing per turn. Pick one from the
dropdown in the chat panel.

A drawing turn, including the retry that really happens in it:

```
you    : "Add a Postgres database below the Auth Service and connect them."
agent  : create_shape  → the canvas answers with the new shape's id
         create_arrow  → rejected: it guessed an id for the shape it just made
         create_arrow  → accepted, using the id the canvas returned
         "Added Postgres below Auth Service and connected them."
```

The agent cannot know a new shape's id in the response that creates it — the browser assigns ids — so
the first arrow is a guess and gets rejected. It corrects on the next pass. It never emits coordinates
either: placement is relative and the frontend computes pixels.

## Running it

```sh
make install
make dev
```

Frontend on http://localhost:5173, server on http://localhost:8787. The canvas fills the window with
the chat panel docked right; a dot in the panel header shows the agent connection. The grid is on by
default (tldraw treats that as temporary state, so turning it off lasts until the next reload).

Claude Code is used automatically if the `claude` CLI is on PATH. For the local model, start it first:

```sh
mlx_lm.server --model mlx-community/Qwen3-30B-A3B-Instruct-2507-8bit --port 8080
WHITEBOARD_AGENT=local make dev
```

Both read the canvas and draw on it. The local one is free and answers in about a second; Claude Code
is slower but better at restructuring a diagram. You can switch mid-session from the dropdown.

## Skills

An agent's canvas knowledge is a markdown file, not a string in the code:
`server/internal/agent/canvas_skill.md`, given identically to both agents.

The chat panel also has a **Skills** row for switching optional skills on and off and writing your own.
Two ship in the binary; yours live in `./skills/*.md` and are gitignored, so they stay on your machine.
Dropping a `.md` file in there by hand works as well as using the UI.

Every enabled skill is resent on every turn and competes with the canvas for context, so the panel
shows what the selection costs. See [`docs/agent-behaviour.md`](docs/agent-behaviour.md) for how this
interacts with the tool loop.

## Layout

| Path | What |
| --- | --- |
| `web/` | Vite + React + tldraw. Owns the canvas and executes agent tools. |
| `server/` | Go. Owns the WebSocket session, the MCP server, and the native agent loop. |
| `server/internal/agent/canvas_skill.md` | How every agent reads and draws. Always applied. |
| `skills/` | Your own skills. Gitignored — they never leave your machine. |
| `spikes/` | Feasibility spikes + `FINDINGS.md`; re-run after a `claude` or `mlx-lm` upgrade. |
| `docs/` | Architecture, agent behaviour, and the dataflow diagram above. |

The build plan and decision log are kept local and unpublished, so the reasoning that matters is
inlined in `docs/` rather than cited by number.

## Configuration

| Variable | Default | Where |
| --- | --- | --- |
| `WHITEBOARD_ADDR` | `:8787` | server |
| `WHITEBOARD_ALLOWED_ORIGINS` | `localhost:5173` | server, comma-separated |
| `WHITEBOARD_MCP_BASE_URL` | `http://127.0.0.1:<port>` | server — where subprocesses call tools back |
| `WHITEBOARD_AGENT` | autodetect | server — `local`, `echo`, or `claude` |
| `WHITEBOARD_LOCAL_BASE_URL` | `http://127.0.0.1:8080/v1` | server — OpenAI-compatible endpoint |
| `WHITEBOARD_LOCAL_MODEL` | `mlx-community/Qwen3-30B-A3B-Instruct-2507-8bit` | server — a real HF repo id, not an alias |
| `WHITEBOARD_SKILLS_DIR` | `skills` | server — where your own skills are read and written |
| `VITE_WS_URL` | `ws://localhost:8787/ws` | web |

Claude Code needs the `claude` CLI on PATH and logged in. The local agent needs `mlx_lm.server`
running. Neither needs an API key.

## Known limits

- **Claude Code turns take tens of seconds.** It is optimised for depth, not chat latency — fine for
  "critique this architecture", sluggish for rapid back-and-forth. Switch to the local agent for that.
- **Its usage shares your normal Claude Code subscription window.** Config isolation makes a turn
  substantially cheaper than the naive setup; `spikes/FINDINGS.md` has the measurement.
- **The local model plans shallowly.** Reliable at one to four calls — add, connect, relabel, delete,
  "what's missing" — and weaker at reorganising an existing diagram.
- **`claude` and `mlx-lm` are both moving dependencies.** After upgrading either, re-run the spikes in
  `spikes/` before trusting sessions.

## Further reading

- [`docs/architecture.md`](docs/architecture.md) — why the agent seam sits above the loop, why the
  browser executes tools, and the invariants that are load-bearing.
- [`docs/agent-behaviour.md`](docs/agent-behaviour.md) — skills, the loop's guards, and what was
  measured about how these models actually draw.
- `spikes/FINDINGS.md` — the measurements themselves, dated and reproducible.

## Licensing note

tldraw is not MIT. The free tier carries a "Made with tldraw" watermark; removing it requires a paid
license. Accepted for a personal tool; it would need revisiting before this shipped as a product.
