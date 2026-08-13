import { useCallback, useRef, useState } from "react";
import type { Editor } from "tldraw";
import { serializeCanvas } from "../canvas/serialize";
import { executeToolCall } from "../canvas/execute";
import type { ClientMessage, ServerMessage, ToolResult } from "./protocol";
import { appendOnce, upsertMessage } from "./transcript";

export interface ChatMessage {
  id: string;
  role: "user" | "assistant";
  text: string;
}

let nextId = 0;
const messageId = () => `m${nextId++}`;

interface Options {
  send: (message: ClientMessage) => boolean;
  editorRef: React.RefObject<Editor | null>;
}

/**
 * Owns chat transcript state and the streaming assembly of assistant turns.
 *
 * Assistant deltas append to the last assistant message rather than creating a
 * new one, so the transcript renders as it streams.
 */
export function useChat({ send, editorRef }: Options) {
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [streaming, setStreaming] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // The dropdown is built from what the server offers, not a hardcoded list.
  const [agents, setAgents] = useState<string[]>([]);
  const [agent, setAgent] = useState<string | null>(null);

  // The id of the assistant message currently being streamed into.
  const activeId = useRef<string | null>(null);
  // The text accumulated for that message. Held here rather than derived from
  // state inside a setMessages updater, which React may invoke more than once.
  const streamText = useRef("");

  // endStream closes off the current assistant message so the next delta starts
  // a new one. Every place that ends a message must reset BOTH refs.
  const endStream = () => {
    activeId.current = null;
    streamText.current = "";
  };

  const handleServerMessage = useCallback(
    (msg: ServerMessage) => {
      switch (msg.type) {
        case "assistant_delta": {
          setStreaming(true);
          // The id is decided BEFORE setMessages, never inside the updater.
          // React invokes updaters twice under StrictMode, so assigning
          // activeId.current in there made the second invocation take the
          // "append to existing" branch, find no such message in prev, and
          // return prev unchanged — silently dropping every delta of the first
          // assistant message in a turn.
          if (activeId.current === null) {
            activeId.current = messageId();
          }
          const id = activeId.current;
          // Accumulate the turn's text in a ref, then render the whole string.
          // Concatenating inside the updater is not safe: React may invoke it
          // twice with the same prev, which would double the delta.
          streamText.current += msg.payload.text;
          const full = streamText.current;
          setMessages((prev) => upsertMessage(prev, id, "assistant", full));
          break;
        }
        case "tool_call": {
          // D5: the browser owns canvas state, so tools execute here and the
          // result goes back over the same socket.
          const editor = editorRef.current;
          const result: ToolResult = editor
            ? executeToolCall(editor, msg.payload)
            : { id: msg.payload.id, ok: false, error: "canvas not ready" };

          // A new assistant message should follow the tool call rather than
          // appending to the text that preceded it.
          endStream();
          send({ type: "tool_result", payload: result });
          break;
        }
        case "turn_end":
          endStream();
          setStreaming(false);
          break;
        case "error":
          // Surfaced in the UI rather than swallowed (plan.md §5.2).
          endStream();
          setStreaming(false);
          setError(msg.payload.message);
          break;
        case "agents_available":
          setAgents(msg.payload.names);
          setAgent(msg.payload.current);
          break;
        case "agent_switched": {
          setAgent(msg.payload.current);
          endStream();
          setStreaming(false);
          // Each agent keeps its own history, so the new one has no memory of
          // what came before. Say so rather than letting the transcript imply
          // continuity that does not exist.
          const name = msg.payload.current;
          const noticeId = messageId();
          setMessages((prev) =>
            appendOnce(prev, {
              id: noticeId,
              role: "assistant",
              text: `— switched to ${name}. It starts with a fresh thread and has not seen the messages above. —`,
            }),
          );
          break;
        }
      }
    },
    [send, editorRef],
  );

  const sendMessage = useCallback(
    (text: string) => {
      const trimmed = text.trim();
      if (!trimmed || streaming) return;

      const editor = editorRef.current;
      const canvas = editor ? serializeCanvas(editor) : undefined;

      const ok = send({
        type: "user_message",
        payload: { text: trimmed, canvas_context: canvas },
      });
      if (!ok) {
        setError("Not connected to the agent.");
        return;
      }

      setError(null);
      setStreaming(true);
      // The id is minted outside the updater: React may invoke it twice, and
      // messageId() inside would append the same message under two ids.
      const id = messageId();
      setMessages((prev) => appendOnce(prev, { id, role: "user", text: trimmed }));
    },
    [send, streaming, editorRef],
  );

  // Stops an in-flight turn. The server cancels the turn context, which
  // unwinds the model stream and any tool call waiting on the browser.
  const cancel = useCallback(() => {
    send({ type: "cancel" });
    setStreaming(false);
  }, [send]);

  // Switching mid-turn would leave the outgoing agent streaming into a
  // transcript that has moved on, so this is guarded here as well as disabled
  // in the UI.
  const switchAgent = useCallback(
    (name: string) => {
      if (streaming || name === agent) return;
      send({ type: "switch_agent", payload: { name } });
    },
    [send, streaming, agent],
  );

  return {
    messages,
    streaming,
    error,
    agents,
    agent,
    sendMessage,
    cancel,
    switchAgent,
    handleServerMessage,
  };
}
