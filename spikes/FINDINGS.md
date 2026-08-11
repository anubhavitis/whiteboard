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

## S3 — Qwen3 tool calling · PASS 10/10

Ran 2026-08-10. **Verdict: pass — the local agent gets tools** (`planv2.md` §0.7
gate is ≥8/10). Harness: `spikes/s3/s3.py`, transcript in `spikes/s3/last_run.json`.

| | |
| --- | --- |
| Model | `mlx-community/Qwen3-30B-A3B-Instruct-2507-8bit` (32.4GB, 8bit) |
| Server | `mlx_lm.server` 0.31.3, Python 3.14, Metal |
| Score | 10/10, ten distinct prompts and ten distinct canvases |
| Tool-call latency | median 1.5s, max 1.9s |
| 8k-canvas turn | 2.9s wall, 9,062 prompt tokens server-reported |

Chose **8bit, not 4bit**: quantization degrades structured output first, and that
is exactly what this gate measures. A false FAIL on a verdict the plan calls FINAL
was the risk worth 32GB.

Three things this spike got wrong before it got it right, all worth keeping:

**1. The first 10/10 was one sample scored ten times.** Re-sending an identical
prompt hit `mlx_lm.server`'s prompt cache: `cached_tokens` 1305 of 1306, byte-identical
tool calls, `completion_tokens=89` on every trial. Temperature 0.7 did not save it.
The harness now varies both the wording (`TASK_VARIANTS`) and the canvas ids
(`ID_PAIRS`) per trial; the honest run produces 10 distinct responses, each copying
its own trial's ids — proof the model reads the canvas rather than replaying.

**2. A transport error reported itself as a model verdict.** With the wrong `model`
name every trial 404'd and the harness printed
`VERDICT: FAIL -> chat/critique-only (FINAL)`. A bad URL could have written off local
tool support permanently. Transport failures now yield `VERDICT: NONE`.

**3. `model` must be a real HF repo id.** `mlx_lm.server` resolves it against the
Hub instead of falling back to what it loaded, so `"local"` 404s with
`Repository Not Found for url: .../models/local`. This was also a live bug in
`internal/native` — see `DefaultModel`.

Infrastructure verified before trusting the score, so the result is attributable to
the model: this model's chat template contains `<tool_call>` and `tool_call.name`, so
`mlx_lm/tokenizer_utils.py:_infer_tool_parser` selects the `json_tools` parser. A
model with no tool support gets a warning and its tools silently dropped
(`server.py:537`); that is not this case.

One caveat on what "pass" means. The model cannot know a new shape's real id in the
same response — the browser assigns it — so it invents a placeholder (`shape:2d8kL`)
for the arrow's far end. Real chaining goes through `tool_result`, exactly as the
Claude Code agent does. Ten of ten got the *canvas-resident* id right, which is the
part that has to be copied; the placeholder half stays unproven until the native
tool loop exists.

Also measured, and it corrects an assumption in this file's own probe: canvas JSON
tokenizes far worse than prose. 260 shapes is **20,891 tokens**, not the ~9.5k a
`len()/4` estimate suggests — off by 2.2x, because of quoted keys and `shape:` ids.
100 shapes is 7,915 tokens, and that is the ~8k fixture now. The §5.3 canvas-diffing
work is not urgent at 8k (2.9s), but a 300-shape brainstorm is a 20k-token prompt.

---

## S3b — the native tool loop, end to end · PASS

Ran 2026-08-11 against the real model through the real WebSocket, with a client
standing in for the browser (assigning ids, rejecting unknown ones) exactly as
`web/src/canvas/execute.ts` does.

| Scenario | Result |
| --- | --- |
| Add a shape and connect it | 3 calls, 3.1s, 1 shape, 1 arrow |
| Flow chart from an empty canvas | 8 calls, 7.1s, 4 shapes, 3 arrows, all chained |
| Rename a shape, then connect to it | 2 calls, 1.8s |
| Delete a shape | 1 call, 1.1s |
| Question only ("don't change anything") | **0 tool calls**, 0.4s |
| "What depends on the Queue?" | correct, reasoned from arrows, no drawing |

**Streaming with tools works, and mlx_lm is easier than OpenAI here.** It emits
each tool call as ONE complete frame — full `name` and complete `arguments` — with
`finish_reason: "tool_calls"`. Real OpenAI fragments `arguments` across deltas.
The parser accumulates by `index` anyway, as insurance for the day the endpoint
changes; against mlx_lm 0.31.3 that branch never fires. This retired the design's
largest unverified assumption (S3 itself only ever used `stream:false`).

**The model always guesses the new shape's id** — every drawing run did it, e.g.
`create_shape` + `create_arrow{to_id:"shape:postgresDb"}` in one response, and the
canvas rejects the arrow. Recovery is one extra iteration and it converges.

**Two wordings of the tool result are load-bearing, both found by measurement:**

1. Told only *"created; use this id"*, the model recovered from the rejected arrow
   by calling `create_shape` **again** — duplicating the box. The result now says
   "this shape now exists — do not create it again", and the duplicate disappeared.
2. A rejection now also carries `shapes_you_created_this_turn`, so the error names
   the ids that do exist rather than only the one that does not.

Neither is cosmetic: without them the loop silently produces duplicate shapes or
unconnected ones. **Server-side id rewriting was rejected** — mapping a guessed id
onto "the shape created just before" means guessing intent and silently drawing
something the user did not ask for. The honest error plus a retry costs ~1.5s and
keeps the model's intent visible.

Zero raw-coordinate violations across every run, so the never-emit-pixels rule
holds under pressure. Arguments are stripped and warned about anyway, because the
frontend ignores unknown keys — the model would otherwise believe it had
positioned something.

---

## Budget note (measured, not projected)

The S1 run reported:

```
"overageStatus":"rejected","overageDisabledReason":"out_of_credits"
```

The five-hour window is already at its overage boundary. At the isolated
$0.0387/turn this is workable for real sessions; at the bare $0.2271 it is not.
This was the strongest argument for finishing S3, which has now passed: a local
agent that costs nothing per turn is available, so `planv2.md` §4.2 (local by
default, escalate to Claude Code) is unblocked. The local agent is chat-only until
the native tool loop is built — the S3 verdict says it *may* have tools, not that
it has them yet.
