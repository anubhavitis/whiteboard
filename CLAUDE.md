# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A local-first AI thinking partner on an infinite canvas. Chat and canvas share one brain: the agent
reads what you've drawn and draws back. React + tldraw frontend, Go agent backend, SQLite persistence.

**`planv2.md` is the current build plan** — `plan.md` is the superseded v1, kept for history.
`DECISIONS.md` holds D1–D7, and `spikes/FINDINGS.md` holds the measured evidence behind D6/D7.
Read them before proposing structural change; they record reasoning not recoverable from the code.

**Current state: `planv2.md` Phases 0–2 complete and verified end to end.** No API key is used or
needed. The agent is a `claude -p` subprocess authenticated by the user's Claude Code subscription;
canvas tools reach it over an MCP server this process hosts.

Verified working (server logs + `spikes/FINDINGS.md`): user message → Claude Code → MCP tool call →
browser executes → result → agent chains the returned shape id into the next call.

`agent.EchoFactory` is the fallback when `claude` is not on PATH, so the canvas still works offline.

## Commands

| Command | Effect |
| --- | --- |
| `make install` | npm install + go mod download |
| `make dev` | Both processes; Go on :8787, vite on :5173 |
| `make lint` | `go vet` + `tsc --noEmit` |
| `make test` | `go test ./...` + `vitest run` |
| `make build` | Go binary to `bin/`, frontend to `web/dist/` |

Single Go test: `cd server && go test ./internal/ws -run TestToolCallRoundTrip -v`
Single web test: `cd web && ./node_modules/.bin/vitest run src/canvas/layout.test.ts`
Race detector (worth running after touching the session or tool loop): `cd server && go test -race ./...`

A `PreToolUse` hook blocks any command matching `npm run dev`, including inside tmux. Use
`./node_modules/.bin/vite` directly when starting the frontend from a tool call.

## Architecture

Two processes talking over one WebSocket per session (D4), mounted at `/ws`.

The agent's loop runs INSIDE Claude Code, not here. Tool calls arrive *into* this process over MCP.

```
browser                     server                          claude -p subprocess
  useAgentSocket ─── WS ───► ws/handler ── SendTurn ────────► stdin (stream-json)
  canvas/serialize ─ JSON ─►                                    │
                                                                │ decides to draw
  canvas/execute  ◄── tool_call ── ws/executor ◄── Execute ── claudecode/mcp  ◄── HTTP
       │                                │                    /mcp/{sessionID}
       └──── tool_result ───────────────┘
```

`internal/agent` defines the boundary (`AgentSession`, `ToolExecutor`, the tool schema).
`internal/claudecode` is the only package that may know about MCP, subprocesses, or the CLI —
that import rule is what makes a local model or an API key a config change later.

**The browser executes tools, not the backend (D5).** The backend streams `tool_call` frames; the
frontend applies them through the tldraw editor API and replies with `tool_result`. This gives the
tldraw store sole ownership of canvas state and makes undo/redo work for free. A backend-side
headless document is a Phase 7 concern, not an optimization to reach for early.

**`protocol.ts` mirrors `protocol.go`.** There is no codegen. Changing a message shape means editing
both files in the same commit; nothing will catch a drift at compile time.

**`agent.AgentSession` is the seam, and it sits ABOVE the loop.** That placement is deliberate:
Claude Code owns its own loop (tool calls arrive via MCP callback), whereas a future native agent
would run its loop here (tool calls parsed out of a stream). An interface at the model-API level
could not span both.

**Agent events forward on their own goroutine, and that is load-bearing.** A tool call blocks until
the browser returns a result, and that result can only arrive through the socket read loop. Handling
turns synchronously deadlocks every drawing turn — this was a real bug, and
`TestToolCallRoundTripDoesNotDeadlock` exists to keep it fixed. Because several goroutines can write
to the socket, all writes go through `session.writeMu`.

**Claude Code subprocess flags are load-bearing too** (`internal/claudecode/session.go`):
`--permission-mode bypassPermissions` — MCP tools are not "edits", and `acceptEdits` makes the
process hang silently with no error; `--strict-mcp-config` + `--setting-sources ""` + an empty
working directory — without these a turn costs ~6x more and drags the user's global plugins, skills
and hooks into a whiteboard conversation; `ToolSearch` must be in `--allowedTools` because Claude
Code may load a tool's schema before calling it. All three cost an hour to find — see
`spikes/FINDINGS.md`.

## Constraints that survive across phases

- **The model never emits raw x/y.** LLMs are bad at absolute pixel coordinates, and this is the
  documented failure mode of every canvas-agent project. Positioning is relative (`near` a shape +
  `direction`) or a coarse grid, with the frontend computing pixels. Non-negotiable.
- **Log canvas-context token size on every message.** Canvas JSON bloats fast; a modest brainstorm
  runs to hundreds of shapes. `canvas_tokens_est` logs on each `user_message`, and `serialize.ts`
  truncates to viewport-visible shapes past ~8k tokens.
- **One `editor.run()` per tool call**, so a single Cmd+Z undoes exactly one agent action.
- **Cap the agent loop.** `agent.MaxToolCallsPerTurn` (15) bounds one turn; the Stop button sends
  `cancel`, which cancels the turn context. An uncapped loop eventually redecorates the whole canvas.
- **Agent-drawn shapes stay visually distinct** from yours.
- **Errors surface in the UI**, never swallowed. The server already reports malformed frames without
  dropping the session, and a bare client close logs at INFO rather than ERROR.

## Diagnosing agent output

The plan's triage, worth keeping: shapes in wrong *places* is a positioning problem (2.2); wrong
*content* is a prompt problem (1.4). Wrong spatial reasoning ("X is above Y" when it isn't) is a
serialization-format problem — fix the serializer before adding drawing capability. Models reason
about structure (arrows and bindings) far better than raw coordinates; lean on bindings.

## Out of scope

Voice input, multiplayer/sync, auth, deployment, mobile, and Excalidraw integration are all
explicitly cut in `plan.md`. The model router is gated on a second consuming project existing —
building it before then is speculative plumbing. Don't add these because they seem natural next steps.
