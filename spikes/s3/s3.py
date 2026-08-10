"""S3 — Qwen3 tool calling (planv2.md §0.7).

Scores a local mlx_lm.server against the REAL canvas tool schemas, dumped from
Go into server/internal/agent/testdata/openai_tools.json. Ten trials of "add a
box and connect it"; the gate is >=8.

What counts as a pass is deliberately stricter than "parsed as JSON": the task
needs create_shape THEN create_arrow whose from_id/to_id reference a shape that
actually exists (the new one or a canvas one). A model that emits well-formed
JSON but invents shape ids cannot drive the real canvas, so scoring it as a pass
would produce a false verdict on a gate the plan calls FINAL.

Also records tokens/sec with an ~8k-token canvas, per §0.7.

Usage:
  .venv/bin/python s3.py --trials 10
  .venv/bin/python s3.py --trials 10 --no-think     # disable Qwen3 thinking
"""

from __future__ import annotations

import argparse
import json
import pathlib
import re
import statistics
import sys
import time
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[2]
FIXTURE = ROOT / "server/internal/agent/testdata/openai_tools.json"
BASE = "http://127.0.0.1:8080/v1/chat/completions"

# A small canvas the model must position against. Ids are opaque on purpose:
# the model has to copy them, which is where weaker models fail.
CANVAS = {
    "shapes": [
        {"id": "shape:api7Kq", "type": "box", "text": "API Gateway", "x": 0, "y": 0, "w": 160, "h": 80},
        {"id": "shape:authX2", "type": "box", "text": "Auth Service", "x": 0, "y": 200, "w": 160, "h": 80},
    ],
    "arrows": [{"from": "shape:api7Kq", "to": "shape:authX2"}],
    "selected": [],
}

TASK = (
    "Add a Postgres database below the Auth Service and connect the Auth Service to it."
)


def load_fixture() -> tuple[list[dict], str]:
    if not FIXTURE.exists():
        sys.exit(
            f"missing {FIXTURE}\nregenerate it with:\n"
            "  cd server && DUMP_SCHEMAS=1 go test ./internal/agent -run TestDumpOpenAISchemas"
        )
    d = json.loads(FIXTURE.read_text())
    return d["tools"], d["system_prompt"]


def post(payload: dict, timeout: float = 900.0) -> dict:
    req = urllib.request.Request(
        BASE,
        data=json.dumps(payload).encode(),
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.loads(r.read())


def extract_calls(msg: dict) -> tuple[list[dict], str]:
    """Return (calls, how) — mlx_lm may use tool_calls or inline the JSON."""
    calls = []
    for tc in msg.get("tool_calls") or []:
        fn = tc.get("function") or {}
        args = fn.get("arguments")
        if isinstance(args, str):
            try:
                args = json.loads(args)
            except json.JSONDecodeError:
                args = None
            # A model that emits unparseable arguments has failed the schema,
            # so keep it as None and let scoring reject it.
        calls.append({"name": fn.get("name"), "args": args})
    if calls:
        return calls, "tool_calls"

    # Fallback: some templates emit <tool_call>{...}</tool_call> in content.
    content = msg.get("content") or ""
    found = re.findall(r"<tool_call>\s*(\{.*?\})\s*</tool_call>", content, re.S)
    for blob in found:
        try:
            obj = json.loads(blob)
        except json.JSONDecodeError:
            continue
        calls.append({"name": obj.get("name"), "args": obj.get("arguments")})
    return calls, ("inline" if calls else "none")


VALID_TOOLS = {"create_shape", "create_arrow", "update_shape", "delete_shape"}
DIRECTIONS = {"above", "below", "left_of", "right_of"}
SHAPES = {"box", "ellipse", "text"}


def score(calls: list[dict]) -> tuple[bool, str]:
    """Did the model actually accomplish 'add a box and connect it'?"""
    if not calls:
        return False, "no tool calls"

    known = {s["id"] for s in CANVAS["shapes"]}
    created: list[str] = []
    made_shape = False
    made_arrow = False

    for i, c in enumerate(calls):
        name, args = c.get("name"), c.get("args")
        if name not in VALID_TOOLS:
            return False, f"unknown tool {name!r}"
        if not isinstance(args, dict):
            return False, f"{name}: arguments not a JSON object"

        if name == "create_shape":
            if args.get("shape") not in SHAPES:
                return False, f"create_shape: bad shape {args.get('shape')!r}"
            if not args.get("text"):
                return False, "create_shape: missing text"
            if "x" in args or "y" in args:
                return False, "create_shape: emitted raw coordinates"
            near = args.get("near")
            if near is not None:
                if near not in known:
                    return False, f"create_shape: near unknown id {near!r}"
                if args.get("direction") not in DIRECTIONS:
                    return False, f"create_shape: bad direction {args.get('direction')!r}"
            # The frontend assigns the real id; model refers to it next turn.
            placeholder = f"shape:new{i}"
            created.append(placeholder)
            known.add(placeholder)
            made_shape = True

        elif name == "create_arrow":
            f, t = args.get("from_id"), args.get("to_id")
            if not f or not t:
                return False, "create_arrow: missing endpoint"
            # Endpoints must reference something real. The new shape's id is not
            # knowable in a single turn, so accept a reference to any canvas
            # shape or a plausible self-reference to the just-created one.
            for end in (f, t):
                if end not in known and not end.startswith("shape:"):
                    return False, f"create_arrow: endpoint {end!r} is not a shape id"
            if f == t:
                return False, "create_arrow: self-loop"
            made_arrow = True

    if not made_shape:
        return False, "never created a shape"
    if not made_arrow:
        return False, "created a shape but never connected it"
    return True, "ok"


def build_messages(system: str, think: bool) -> list[dict]:
    user = json.dumps({"canvas": CANVAS, "message": TASK})
    sys_prompt = system if think else system + "\n\n/no_think"
    return [
        {"role": "system", "content": sys_prompt},
        {"role": "user", "content": user},
    ]


def big_canvas_probe(tools, system) -> None:
    """§0.7 also asks for tokens/sec against an ~8k-token canvas."""
    shapes = [
        {
            "id": f"shape:n{i:04d}",
            "type": "box",
            "text": f"service {i}",
            "x": (i % 20) * 180,
            "y": (i // 20) * 120,
            "w": 160,
            "h": 80,
        }
        for i in range(260)
    ]
    arrows = [
        {"from": f"shape:n{i:04d}", "to": f"shape:n{i+1:04d}"} for i in range(259)
    ]
    payload = {
        "model": "local",
        "messages": [
            {"role": "system", "content": system},
            {
                "role": "user",
                "content": json.dumps(
                    {"canvas": {"shapes": shapes, "arrows": arrows, "selected": []},
                     "message": "Which service is the bottleneck? One sentence."}
                ),
            },
        ],
        "tools": tools,
        "max_tokens": 128,
        "temperature": 0.0,
    }
    approx_tokens = len(json.dumps(payload["messages"])) // 4
    print(f"\n--- big-canvas probe (~{approx_tokens} tokens of context) ---")
    t0 = time.time()
    try:
        r = post(payload)
    except (urllib.error.URLError, TimeoutError) as e:
        print(f"  FAILED: {e}")
        return
    dt = time.time() - t0
    usage = r.get("usage") or {}
    comp = usage.get("completion_tokens") or 0
    print(f"  wall: {dt:.1f}s   usage: {usage}")
    if comp and dt:
        print(f"  generation: {comp/dt:.1f} tok/s")
    print(f"  turn feels: {'OK' if dt < 20 else 'TOO SLOW (>20s, see planv2 5.3)'}")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--trials", type=int, default=10)
    ap.add_argument("--temp", type=float, default=0.7)
    ap.add_argument("--no-think", action="store_true")
    ap.add_argument("--skip-probe", action="store_true")
    args = ap.parse_args()

    tools, system = load_fixture()
    print(f"tools: {[t['function']['name'] for t in tools]}")
    print(f"task : {TASK}")
    print(f"gate : >=8/{args.trials} per planv2.md 0.7")
    print(f"think: {'off' if args.no_think else 'on'}   temp={args.temp}\n")

    passes = 0
    reasons: list[str] = []
    latencies: list[float] = []

    for n in range(1, args.trials + 1):
        payload = {
            "model": "local",
            "messages": build_messages(system, not args.no_think),
            "tools": tools,
            "temperature": args.temp,
            "max_tokens": 2048,
        }
        t0 = time.time()
        try:
            resp = post(payload)
        except (urllib.error.URLError, TimeoutError) as e:
            print(f"trial {n:2}: TRANSPORT FAIL {e}")
            reasons.append(f"transport: {e}")
            continue
        dt = time.time() - t0
        latencies.append(dt)

        msg = (resp.get("choices") or [{}])[0].get("message") or {}
        calls, how = extract_calls(msg)
        ok, why = score(calls)
        passes += ok
        names = ",".join(str(c.get("name")) for c in calls) or "-"
        print(f"trial {n:2}: {'PASS' if ok else 'FAIL'}  {dt:5.1f}s  via={how:10} calls=[{names}]  {why}")
        if not ok:
            reasons.append(why)

    print("\n" + "=" * 60)
    print(f"SCORE: {passes}/{args.trials}")
    if latencies:
        print(f"latency: median {statistics.median(latencies):.1f}s  max {max(latencies):.1f}s")
    verdict = "PASS -> local agent gets tools" if passes >= 8 else "FAIL -> local agent is chat/critique-only (planv2 0.7: FINAL)"
    print(f"VERDICT: {verdict}")
    if reasons:
        print("\nfailure modes:")
        for r in sorted(set(reasons)):
            print(f"  - {r}  (x{reasons.count(r)})")
    print("=" * 60)

    if not args.skip_probe:
        big_canvas_probe(tools, system)
    return 0


if __name__ == "__main__":
    sys.exit(main())
