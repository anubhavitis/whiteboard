# Whiteboard Partner

A local-first AI thinking partner on an infinite canvas. Chat and canvas share one brain: the agent
can read what you've drawn and draw back.

**Stack:** React + tldraw · Go (session hub, MCP server) · two interchangeable agents: a Claude Code subprocess (no API key) and a local MLX/Qwen3 model

Canvas state lives in the browser's tldraw store and persists there; the server keeps no database.

## Status

Phases 0–2 of the build plan complete and verified end to end, plus the native tool loop (§3.6) and a
per-session skills picker (§1.2). Either agent reads the canvas and draws on it: adds shapes, connects
them with arrows, relabels, deletes. Agent-drawn shapes are violet so you
can tell who drew what.

**No API key needed, either way.** Claude Code runs as a `claude -p` subprocess authenticated by your
subscription, with canvas tools reaching it through an MCP server the Go backend hosts. The local agent
talks to `mlx_lm.server` on your own machine and costs nothing per turn. Pick one from the dropdown in
the chat panel; if neither is available the canvas still works and the agent echoes your text back.

A real turn, captured from the running server:

```
you    : "Add a Postgres database below the API Gateway and connect them."
agent  : create_shape {"shape":"box","text":"Postgres","near":"shape:api","direction":"below"}
         create_arrow {"from_id":"shape:api","to_id":"shape:gen1"}
         "…one thing worth noting: a gateway talking straight to a database is unusual."
```

Note it never emits coordinates — placement is relative, and the frontend computes pixels. Shapes are
`box`, `ellipse`, `diamond` (a decision point) or `text`.

## How a turn flows

Where the agent's loop runs depends on which agent you picked: *inside* Claude Code, with tool calls
arriving back over MCP, or inside the Go server, with tool calls parsed out of the model's stream.
Either way the browser — not the backend — applies them to the canvas (D5), so the tldraw store stays
the single owner of canvas state and undo/redo works for free.

<p align="center">
  <img src="docs/turn-flow.svg" alt="A turn flows from the browser to ws/handler, out to whichever agent is running, and back to ws/executor, which asks the browser to execute each canvas tool. Claude Code returns tool calls over MCP; the local MLX model has them parsed out of its stream." width="100%">
</p>

Grey is the chat path, violet the drawing path. Results travel back the way they came: a shape id
returns through `ws/executor` to whichever agent asked, and the agent's text streams out to the chat
panel as it arrives.

### Two agents, one interface

The second agent is the reason `agent.AgentSession` is shaped the way it is. The two put the loop on
**opposite sides of the process boundary**:

| | Claude Code | MLX / Qwen3 |
| --- | --- | --- |
| Who owns the loop | Claude Code itself | our Go code (`internal/native`) |
| How tool calls arrive | pushed *in* over MCP | parsed *out of* the stream |
| Transport | subprocess stdio + HTTP callback | OpenAI-compatible HTTP to `mlx_lm.server` |
| Auth | your Claude Code subscription | none — runs on your machine |
| Multi-turn memory | the subprocess, via `--resume` | ours: the endpoint is stateless |
| Status | reads and draws | reads and draws |

That is why the seam sits *above* the loop rather than at the model-API level: an interface at the
API level could not span both. Both implementations reach the canvas through the same
`agent.ToolExecutor`, so the browser-executes-tools half of the diagram is unchanged either way —
only the left edge differs. Nothing outside the two agent implementations may import
`anthropic`/`openai`/`mcp`/`exec` packages; that single import rule is what keeps this swappable.

**Both agents now read and draw.** Measured end to end against the real model (S3b in
`spikes/FINDINGS.md`): add-and-connect in 3 calls / 3.1s, a four-step flow chart from an empty canvas
in 8 calls / 7.1s, rename-and-connect in 2 calls / 1.8s, and **zero** tool calls when the question was
"just answer, don't change anything."

Two findings there are worth reading before touching the loop. The model **always** guesses the new
shape's id — it cannot know one, since the browser assigns them — so the arrow in its first response
gets rejected and it corrects on the next iteration. And the wording of a tool result is load-bearing:
told only "use this id", it recovered from that rejection by creating the shape a *second* time. The
result now says the shape already exists, and a rejection lists the ids that do.

The violet loop repeats for each tool the agent calls, chaining returned shape ids into later calls —
that is how `create_arrow` knows what to connect. Two properties of it are load-bearing:

- **The tool call blocks the whole way round.** It waits on the browser, and the reply can only
  arrive through the socket read loop — so turns are forwarded on their own goroutine. Handling them
  synchronously deadlocks every drawing turn; `TestToolCallRoundTripDoesNotDeadlock` keeps it fixed.
  The round-trip is bounded by a 30s timeout, so a closed tab fails the call instead of wedging the
  session.
- **The loop is capped** at `agent.MaxToolCallsPerTurn` (15), and Stop sends `cancel`, which cancels
  the turn context. An uncapped agent eventually redecorates the whole canvas.

## Running it

```sh
make install
make dev
```

Frontend on http://localhost:5173, server on http://localhost:8787. The canvas fills the window with
the chat panel docked right; a dot in the panel header shows the agent connection — amber connecting,
green connected, red disconnected. The grid is on by default (tldraw treats that as temporary state, so
turning it off lasts until the next reload).

### Running against a local model

`WHITEBOARD_AGENT` picks the agent; leaving it unset autodetects (Claude Code if `claude` is on PATH,
otherwise echo). The local agent talks to any OpenAI-compatible endpoint:

```sh
mlx_lm.server --model mlx-community/Qwen3-30B-A3B-Instruct-2507-8bit --port 8080
WHITEBOARD_AGENT=local make dev
```

It reads the canvas **and draws on it**, same as Claude Code — free, private, and roughly 1–7s per
turn depending on how much it draws.

## Skills

What an agent knows about the canvas is a markdown file, not a string in the code:
`server/internal/agent/canvas_skill.md`. Both agents get it identically — Claude Code via
`--append-system-prompt`, the local model as a system message — so the two can never drift.

On top of that, the chat panel has a **Skills** row where you can switch optional skills on and off,
and write your own. Two built-ins ship in the binary (flow-chart conventions, system-design
diagrams). Your own live in `./skills/*.md` and are **gitignored**: they are your notes on how you
want your agent to behave, not project source, so they never arrive in anyone else's clone. Dropping
a `.md` file in there by hand works exactly as well as using the UI.

The core canvas skill is deliberately absent from the picker. Its rules are the ones the code
enforces — never emit coordinates, ids are not names, never claim an edit that did not happen — so an
agent without it does not read as "fewer skills", it reads as broken.

Two things worth knowing:

- **Changing skills restarts the agent.** A system prompt is fixed when a session starts, so there is
  no other way to apply one. That means a fresh thread, the same trade-off as switching agents, and
  the picker is disabled mid-turn for the same reason.
- **Every enabled skill is resent on every turn** and competes with the canvas for context. The panel
  shows the running cost — the core skill is ~1,440 tokens and each extra one ~250, against a canvas
  budget of 8,000 — because a slow, vaguer agent is the symptom of overloading it, and that is worth
  seeing rather than guessing at.

## Layout

| Path | What |
| --- | --- |
| `web/` | Vite + React + tldraw. Owns the canvas and executes agent tools. |
| `server/` | Go. Owns the model loop and the WebSocket session. |
| `spikes/` | Feasibility spikes + `FINDINGS.md`; re-run after a `claude` or `mlx-lm` upgrade. |
| `docs/turn-flow.svg` | The dataflow diagram above, hand-authored. Edit it when the protocol changes. |
| `server/internal/agent/canvas_skill.md` | How every agent reads and draws on the canvas. Always applied. |
| `server/internal/agent/skills/` | Built-in optional skills, embedded in the binary. |
| `skills/` | Your own skills. Gitignored — they never leave your machine. |

The build plan (`planv2.md`) and the decision log (`DECISIONS.md`, D1–D9) are kept local and are not
published here; references to `D2`/`D5`/`D8`/`D9` and `planv2` sections point at those.

## Configuration

| Variable | Default | Where |
| --- | --- | --- |
| `WHITEBOARD_ADDR` | `:8787` | server |
| `WHITEBOARD_ALLOWED_ORIGINS` | `localhost:5173` | server, comma-separated |
| `WHITEBOARD_MCP_BASE_URL` | `http://127.0.0.1:<port>` | server — where subprocesses call tools back |
| `WHITEBOARD_AGENT` | autodetect | server — `local`, `echo`, or `claude` |
| `WHITEBOARD_LOCAL_BASE_URL` | `http://127.0.0.1:8080/v1` | server — OpenAI-compatible endpoint |
| `WHITEBOARD_LOCAL_MODEL` | `…Qwen3-30B-A3B-Instruct-2507-8bit` | server — must be a real HF repo id, not an alias |
| `WHITEBOARD_SKILLS_DIR` | `skills` | server — where your own skills are read and written |
| `VITE_WS_URL` | `ws://localhost:8787/ws` | web |

Claude Code needs the `claude` CLI on PATH and logged in. The local agent needs `mlx_lm.server`
running. Neither needs an API key.

## Known limits

- **Claude Code turns take ~10–30s.** It is optimised for depth, not chat latency. Fine for "critique
  this architecture", sluggish for rapid back-and-forth — switch to the local agent, which answers in
  ~1s and draws in a few.
- **Usage is shared with your normal Claude Code work** — same subscription window. Config isolation
  keeps a turn ~6x cheaper than the naive setup; see `spikes/FINDINGS.md`.
- **The `claude` CLI is a moving dependency.** After upgrading it, re-run the spikes in `spikes/`
  before trusting sessions (planv2 §5.5). Verified against 2.1.228.
- **`mlx-lm` is one too.** The tool-call format it emits is version-specific: 0.31.3 sends each call
  whole in one SSE frame rather than fragmenting arguments the way the OpenAI API does. The parser
  handles both, but re-run `spikes/s3` after upgrading.
- **The local model plans shallowly.** It is reliable at one to four calls — add, connect, relabel,
  delete, "what's missing" — and weaker when asked to restructure an existing diagram. It also guesses
  shape ids on its first attempt and burns two or three rejected arrows correcting itself, which costs
  a couple of seconds. Claude Code is the better choice for "re-architect this".

## Licensing note

tldraw is not MIT. The free tier carries a "Made with tldraw" watermark; removing it requires a paid
license. Accepted for a personal tool — see D2 in `DECISIONS.md` if this ever ships as a product.
