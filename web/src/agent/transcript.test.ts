import { describe, expect, it } from "vitest";
import { appendOnce, dropEmpty, upsertMessage } from "./transcript";
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

describe("whitespace-only assistant text", () => {
  // Regression: a drawing turn streams a bare "\n" before its tool calls
  // (measured on every s3 trial: content == "\n"). Rendering that created a
  // message whose text was whitespace, which showed as an empty bubble in the
  // transcript wherever the agent drew something.
  it("creates no message for whitespace-only text", () => {
    expect(upsertMessage([], "m1", "assistant", "\n")).toEqual([]);
    expect(upsertMessage([], "m1", "assistant", "   ")).toEqual([]);
  });

  it("removes a message that becomes whitespace-only", () => {
    const prev: ChatMessage[] = [{ id: "m1", role: "assistant", text: "\n" }];
    expect(upsertMessage(prev, "m1", "assistant", "\n")).toEqual([]);
  });

  it("still renders text that merely starts with a newline", () => {
    const out = upsertMessage([], "m1", "assistant", "\nAdded the diamond.");
    expect(out).toHaveLength(1);
    expect(out[0].text).toBe("\nAdded the diamond.");
  });

  it("keeps other messages when dropping an empty one", () => {
    const prev: ChatMessage[] = [
      { id: "u1", role: "user", text: "draw it" },
      { id: "m1", role: "assistant", text: "\n" },
    ];
    expect(upsertMessage(prev, "m1", "assistant", " ")).toEqual([
      { id: "u1", role: "user", text: "draw it" },
    ]);
  });
});

describe("dropEmpty", () => {
  it("removes a whitespace-only message by id", () => {
    const prev: ChatMessage[] = [
      { id: "u1", role: "user", text: "hi" },
      { id: "m1", role: "assistant", text: "\n" },
    ];
    expect(dropEmpty(prev, "m1")).toEqual([{ id: "u1", role: "user", text: "hi" }]);
  });

  it("leaves a message that has real text", () => {
    const prev: ChatMessage[] = [{ id: "m1", role: "assistant", text: "Added it." }];
    expect(dropEmpty(prev, "m1")).toEqual(prev);
  });

  it("is a no-op for a null id", () => {
    const prev: ChatMessage[] = [{ id: "m1", role: "assistant", text: "x" }];
    expect(dropEmpty(prev, null)).toEqual(prev);
  });

  it("is idempotent under a double invocation", () => {
    const prev: ChatMessage[] = [{ id: "m1", role: "assistant", text: "\n" }];
    expect(twice(prev, (p) => dropEmpty(p, "m1"))).toEqual([]);
  });
});
