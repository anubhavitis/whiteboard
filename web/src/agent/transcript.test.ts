import { describe, expect, it } from "vitest";
import { appendOnce, upsertMessage } from "./transcript";
import type { ChatMessage } from "./useChat";

/**
 * React invokes state updaters twice under StrictMode. `twice` applies an
 * updater the way React might, feeding it the SAME prev both times, which is
 * exactly what broke streaming: the second pass silently returned prev.
 */
function twice(
  prev: ChatMessage[],
  fn: (p: ChatMessage[]) => ChatMessage[],
): ChatMessage[] {
  fn(prev);
  return fn(prev);
}

describe("upsertMessage", () => {
  it("creates the message when it is not there", () => {
    const out = upsertMessage([], "m1", "assistant", "Hello");
    expect(out).toEqual([{ id: "m1", role: "assistant", text: "Hello" }]);
  });

  it("survives a double invocation with the same prev", () => {
    // The regression: the first assistant delta of a turn used to disappear.
    const out = twice([], (p) => upsertMessage(p, "m1", "assistant", "Hello"));
    expect(out).toEqual([{ id: "m1", role: "assistant", text: "Hello" }]);
  });

  it("replaces text rather than concatenating, so a repeat cannot double it", () => {
    const prev: ChatMessage[] = [{ id: "m1", role: "assistant", text: "Hel" }];
    const out = twice(prev, (p) => upsertMessage(p, "m1", "assistant", "Hello"));
    expect(out).toEqual([{ id: "m1", role: "assistant", text: "Hello" }]);
  });

  it("leaves other messages alone", () => {
    const prev: ChatMessage[] = [
      { id: "u1", role: "user", text: "hi" },
      { id: "m1", role: "assistant", text: "He" },
    ];
    const out = upsertMessage(prev, "m1", "assistant", "Hello");
    expect(out[0]).toEqual({ id: "u1", role: "user", text: "hi" });
    expect(out[1].text).toBe("Hello");
  });

  it("accumulates across deltas when given growing text", () => {
    let msgs: ChatMessage[] = [];
    for (const full of ["I", "I see", "I see a box"]) {
      msgs = upsertMessage(msgs, "m1", "assistant", full);
    }
    expect(msgs).toHaveLength(1);
    expect(msgs[0].text).toBe("I see a box");
  });
});

describe("appendOnce", () => {
  it("appends a new message", () => {
    const out = appendOnce([], { id: "u1", role: "user", text: "hi" });
    expect(out).toHaveLength(1);
  });

  it("does not duplicate on a double invocation", () => {
    const out = twice([], (p) =>
      appendOnce(p, { id: "u1", role: "user", text: "hi" }),
    );
    expect(out).toHaveLength(1);
  });

  it("keeps distinct messages", () => {
    let msgs = appendOnce([], { id: "u1", role: "user", text: "one" });
    msgs = appendOnce(msgs, { id: "u2", role: "user", text: "two" });
    expect(msgs.map((m) => m.text)).toEqual(["one", "two"]);
  });
});
