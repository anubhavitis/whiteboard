# CLAUDE.md

Guidance for Claude Code (claude.ai/code) working in this repository.

## What this is

A local-first AI thinking partner on an infinite canvas. Chat and canvas share one brain: the agent
reads what you've drawn and draws back. React + tldraw frontend, Go backend, two interchangeable
agents. Canvas state lives in the browser's tldraw store; there is no database.

**Read these before proposing structural change** — they record reasoning that is not recoverable from
the code:

| File | What it holds | Public? |
| --- | --- | --- |
| `docs/architecture.md` | The agent seam, the tool loop's invariants, subprocess flags, positioning | yes |
| `docs/agent-behaviour.md` | Skills, the loop's guards, measured model behaviour | yes |
| `spikes/FINDINGS.md` | The measurements themselves, dated | yes |
| `DECISIONS.md` | D1–D9, the decisions expensive to reverse | no, gitignored |
| `planv2.md` | The current build plan (`plan.md` is the superseded v1) | no, gitignored |

Do not duplicate those files here. Two copies of a rationale is how the last round of documentation
drift happened: the README and this file both described the tool loop, and both went stale
independently.

## Commands

| Command | Effect |
| --- | --- |
| `make install` | npm install + go mod download |
| `make dev` | Both processes; Go on :8787, vite on :5173 |
| `make lint` | `go vet` + `tsc --noEmit` |
| `make test` | `go test ./...` + `vitest run` |
| `make build` | Go binary to `bin/`, frontend to `web/dist/` |

Single Go test: `cd server && go test ./internal/ws -run TestToolCallRoundTripDoesNotDeadlock -v`
Single web test: `cd web && ./node_modules/.bin/vitest run src/canvas/layout.test.ts`
Race detector, worth running after touching the session or tool loop: `cd server && go test -race ./...`

A `PreToolUse` hook blocks any command matching `npm run dev`, including inside tmux. Use
`./node_modules/.bin/vite` directly when starting the frontend from a tool call.

## Rules that are easy to break from a tool call

**`protocol.ts` mirrors `protocol.go`, with no codegen.** Changing a message shape means editing both
files in the same commit. Nothing catches a drift at compile time.

**A Go nil slice marshals to JSON `null`, and `null.length` throws in the browser.** Any field the
frontend iterates must be sent as an empty array. This crashed the whole UI once.

**Never state a value in prose that exists in code.** Cite the identifier —
`agent.MaxToolCallsPerTurn`, `ws.toolTimeout`, `MAX_CONTEXT_TOKENS` — never the number. A named
constant survives a change; a prose number silently rots. `server/internal/docs` tests enforce parts of
this.

**The model never emits raw x/y.** Positioning is relative (`near` + `direction`) or a coarse grid, with
the frontend computing pixels. Non-negotiable — it is the documented failure mode of canvas-agent
projects, and it is enforced in the schema, the loop, and a test.

**One `editor.run()` per tool call**, so a single Cmd+Z undoes exactly one agent action.

**Errors surface in the UI, never swallowed.** A malformed frame is reported without dropping the
session; a bare client close logs at INFO rather than ERROR.

**Agent-drawn shapes stay visually distinct** from the user's.

## Testing expectations

Fakes must fail the way the real thing fails. Two bugs shipped because they did not:

- the serializer's test fixtures used `props.text`, a shape tldraw never produces, so a serializer that
  could not read real labels passed its tests
- the fake editor records props without validating them, so an illegal `richText` on an arrow — which
  crashes the real tldraw store — passed too

When a bug is found, the test that would have caught it goes in the same commit. Prefer asserting on
the wire bytes or the actual prop names over asserting on a fake's bookkeeping.

## Working with the agents

`agent.EchoFactory` is the fallback when neither agent is available, so the canvas still works offline.

Verifying a change to drawing behaviour needs a real model, not a fake: `spikes/s3/` has the harness,
and the model is already on disk. A fake SSE server proves the loop's shape; only the real model proves
the prompt works.

## Out of scope

Voice input, multiplayer/sync, auth, deployment, mobile, and Excalidraw integration are cut
deliberately. The model router is gated on a second consuming project existing. Don't add these because
they seem like natural next steps.
