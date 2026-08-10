# Spike findings

Evidence for the go/no-go gates in `planv2.md` §0.5–0.7. Re-run these after any
`claude` upgrade (planv2.md §5.5) — the CLI is an explicitly moving dependency.

Environment: Claude Code **2.1.226**, macOS (Darwin 25.6.0), subscription auth
(`apiKeySource: "none"`), no `ANTHROPIC_API_KEY` and no `~/.config/anthropic`
profile.

---

## S1 — persistent `claude -p` pipe · **PASS**

Run: `cd spikes/s1 && go run .`

| Question | Result |
|---|---|
| Subprocess accepts stream-json turns | ✅ yes |
| **One process serves MANY turns, with memory** | ✅ 3 turns, 47 → recalled → 50 |
| `session_id` available from the stream | ✅ from every event, incl. `system/init` |
| `--resume <id>` restores memory after process exit | ✅ answered "47" from a fresh process |
| Config isolation reduces per-turn cost | ✅ **83%** — $0.2271 → $0.0387 |

### The persistent pipe amortizes setup — this is why it's worth the complexity

Three turns on one process (`go test -run TestOneProcessServesMultipleTurns`):

| turn | cache_creation | cost |
|---|---|---|
| 1 | 5,635 | $0.0651 |
| 2 | **25** | $0.0763 |
| 3 | **21** | $0.0875 |

After turn 1 the prefix is cached and later turns rebuild almost nothing. A
process-per-turn design would pay the 5.6k prefix every time; the long-lived
pipe pays it once per session. Cost drifts up only as the conversation grows,
which is the expected shape.

**Isolation is not optional.** A bare invocation drags the user's entire global
Claude Code world into a whiteboard turn:

| | cost | cache_creation tokens |
|---|---|---|
| bare | $0.2271 | 21,896 |
| isolated | $0.0387 | 2,857 |

The isolating flags, all verified present on 2.1.226:

```
--strict-mcp-config          # ignore globally-configured MCP servers
--setting-sources ""         # load no user/project/local settings
--append-system-prompt ...   # our prompt, not the coding-agent default
--max-turns N                # bounds the PROCESS, not one exchange (see below)
```

…plus an empty working directory (`cmd.Dir`), so no `CLAUDE.md` or
`SessionStart` hook is discovered.

### Gotcha: `--max-turns` bounds the process, not the exchange

`--max-turns 1` ends the whole session after one assistant turn, so the pipe
cannot serve a second user message. For a long-lived session pipe, omit it and
enforce the loop cap ourselves. Use it only for one-shot probes.

### Latency is noisy — do not tune on one sample

Observed first-token times across runs: 2.9s, 8.9s, 28s, 91s. The 91s outlier
was an isolated run and the 28s a bare one, so the ordering contradicts the cost
ordering. Cold start and service contention dominate; treat latency as
unmeasured until sampled properly under load.

---

## S2 — MCP callback with per-session routing · **PASS**

Run: `cd spikes/s2 && go run .` (diagnostic: `cd spikes/s2/diag && go run .`)

| Question | Result |
|---|---|
| Headless claude discovers a custom Go MCP server | ✅ `status=connected` |
| Tool appears in the session's tool list | ✅ `mcp__canvas__create_shape` |
| Tool call round-trips (call → our server → result → model) | ✅ |
| **Per-session routing via `/mcp/{sessionID}`** | ✅ correct label per session |
| **Two concurrent subprocesses stay isolated** | ✅ no cross-talk |

Observed:

```
session=alpha  tool=create_shape  args=map[text:Alpha Service]
session=beta   tool=create_shape  args=map[text:Beta Service]
```

### Three findings that cost an hour — do not rediscover them

1. **`--permission-mode acceptEdits` hangs on MCP tools.** MCP calls are not
   "edits", so the mode never grants them; the subprocess connects, discovers
   the tool, then blocks forever with no error. Use `bypassPermissions` (the
   tools are ours and the sandbox is the browser). This was the entire cause of
   the first run's zero tool calls.
2. **Claude Code may call `ToolSearch` before the real tool**, to load the
   schema on demand. Allow it (`--allowedTools "mcp__canvas__*,ToolSearch"`)
   and leave headroom in `--max-turns`, or the turn dies mid-search.
3. **`notifications/initialized` arrives with an empty body.** Treat a decode
   failure as a notification and return 202; do not 400 it.

### MCP without a dependency

`mark3labs/mcp-go` v0.57 requires a Go toolchain upgrade (a download this
machine's connection could not sustain). MCP over HTTP is JSON-RPC 2.0 and the
three methods we need — `initialize`, `tools/list`, `tools/call` — are ~80 lines
of stdlib. That is what the spike implements, and it also makes the
"nothing outside the agent package imports mcp" rule free.

Client protocol version observed: `2025-11-25`. Server replying `2024-11-05`
was accepted.

---

## S3 — Qwen3 tool calling · deferred

Requires `mlx-lm` and a ~17GB model pull; deferred on bandwidth grounds
(user decision, this session). Until it runs, **D6 is decided by S1/S2 alone**
and the native agent slot stays wired but empty.

Consequence to accept knowingly: with no local agent, every whiteboard turn
draws on the same subscription window as real work. `planv2.md` §4.2
(local default, escalate to Claude) is the mitigation and is currently
unavailable.

---

## Budget note (measured, not projected)

The S1 run reported:

```
"overageStatus":"rejected","overageDisabledReason":"out_of_credits"
```

The five-hour window is already at its overage boundary. At the isolated
$0.0387/turn this is workable for real sessions; at the bare $0.2271 it is not.
This is the strongest argument for finishing S3 when bandwidth allows.
