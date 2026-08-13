import type { ChatMessage } from "./useChat";

/**
 * Transcript updates, as pure functions.
 *
 * These exist as their own module because React may invoke a `setState` updater
 * more than once for a single event — StrictMode does it deliberately in
 * development. An updater that mutates a ref or mints an id therefore behaves
 * differently on its second invocation, and the difference is silent.
 *
 * That is not hypothetical: assigning the active message id *inside* the updater
 * meant the second invocation took the "append to an existing message" branch,
 * found no such message in `prev`, and returned `prev` unchanged. Every delta of
 * an assistant turn vanished, with no error anywhere.
 *
 * Every function here must be idempotent: calling it twice with the same
 * arguments must produce the same result as calling it once.
 */

/**
 * upsert renders the streaming assistant message with its full text so far.
 *
 * Text that is only whitespace produces no message at all, and empties an
 * existing one. Models routinely emit a bare "\n" before their tool calls — it is
 * the usual content of a drawing turn (measured: `content: "\n"` on every trial
 * in spikes/s3/last_run.json) — and rendering that as a message left an empty
 * bubble in the transcript wherever the agent drew something.
 */
export function upsertMessage(
  prev: ChatMessage[],
  id: string,
  role: ChatMessage["role"],
  text: string,
): ChatMessage[] {
  if (text.trim() === "") {
    // Nothing worth showing. Drop the message if it was already created, so a
    // turn that starts with whitespace and then draws leaves no blank bubble.
    return prev.some((m) => m.id === id)
      ? prev.filter((m) => m.id !== id)
      : prev;
  }
  if (!prev.some((m) => m.id === id)) {
    return [...prev, { id, role, text }];
  }
  return prev.map((m) => (m.id === id ? { ...m, text } : m));
}

/** dropEmpty removes a message that ended a turn with nothing in it. */
export function dropEmpty(
  prev: ChatMessage[],
  id: string | null,
): ChatMessage[] {
  if (id === null) return prev;
  return prev.filter((m) => !(m.id === id && m.text.trim() === ""));
}

/** appendOnce adds a message unless one with the same id is already present. */
export function appendOnce(
  prev: ChatMessage[],
  message: ChatMessage,
): ChatMessage[] {
  return prev.some((m) => m.id === message.id) ? prev : [...prev, message];
}
