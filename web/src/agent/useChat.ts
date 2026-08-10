import { useCallback, useRef, useState } from "react";
import type { Editor } from "tldraw";
import { serializeCanvas } from "../canvas/serialize";
import { executeToolCall } from "../canvas/execute";
import type { ClientMessage, ServerMessage, ToolResult } from "./protocol";

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

  // The id of the assistant message currently being streamed into.
  const activeId = useRef<string | null>(null);

  const handleServerMessage = useCallback(
    (msg: ServerMessage) => {
    switch (msg.type) {
      case "assistant_delta": {
        setStreaming(true);
        setMessages((prev) => {
          if (activeId.current === null) {
            const id = messageId();
            activeId.current = id;
            return [...prev, { id, role: "assistant", text: msg.payload.text }];
          }
          return prev.map((m) =>
            m.id === activeId.current
              ? { ...m, text: m.text + msg.payload.text }
              : m,
          );
        });
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
        activeId.current = null;
        send({ type: "tool_result", payload: result });
        break;
      }
      case "turn_end":
        activeId.current = null;
        setStreaming(false);
        break;
      case "error":
        // Surfaced in the UI rather than swallowed (plan.md §5.2).
        activeId.current = null;
        setStreaming(false);
        setError(msg.payload.message);
        break;
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
      setMessages((prev) => [
        ...prev,
        { id: messageId(), role: "user", text: trimmed },
      ]);
    },
    [send, streaming, editorRef],
  );

  // Stops an in-flight turn. The server cancels the turn context, which
  // unwinds the model stream and any tool call waiting on the browser.
  const cancel = useCallback(() => {
    send({ type: "cancel" });
    setStreaming(false);
  }, [send]);

  return { messages, streaming, error, sendMessage, cancel, handleServerMessage };
}
