import { useEffect, useRef, useState } from "react";
import type { ChatMessage } from "../agent/useChat";
import type { ConnectionStatus } from "../agent/protocol";
import { ConnectionDot } from "./ConnectionDot";

interface Props {
  messages: ChatMessage[];
  streaming: boolean;
  error: string | null;
  status: ConnectionStatus;
  onSend: (text: string) => void;
  onCancel: () => void;
}

export function ChatPanel({
  messages,
  streaming,
  error,
  status,
  onSend,
  onCancel,
}: Props) {
  const [draft, setDraft] = useState("");
  const listRef = useRef<HTMLDivElement>(null);

  // Follow the tail as tokens stream in.
  useEffect(() => {
    const list = listRef.current;
    if (list) list.scrollTop = list.scrollHeight;
  }, [messages]);

  function submit(event: React.FormEvent) {
    event.preventDefault();
    onSend(draft);
    setDraft("");
  }

  return (
    <aside className="chat">
      <header className="chat__header">
        <span>Thinking partner</span>
        <ConnectionDot status={status} />
      </header>

      <div className="chat__messages" ref={listRef}>
        {messages.length === 0 && (
          <p className="chat__empty">
            Draw something, then ask what you're missing.
          </p>
        )}
        {messages.map((message) => (
          <div key={message.id} className={`chat__message chat__message--${message.role}`}>
            {message.text}
          </div>
        ))}
        {error && <div className="chat__error">{error}</div>}
      </div>

      <form className="chat__composer" onSubmit={submit}>
        <textarea
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            // Enter sends; Shift+Enter is a newline.
            if (e.key === "Enter" && !e.shiftKey) submit(e);
          }}
          placeholder={streaming ? "Thinking…" : "Ask about the canvas…"}
          rows={3}
          disabled={streaming}
        />
        {streaming ? (
          <button type="button" className="chat__stop" onClick={onCancel}>
            Stop
          </button>
        ) : (
          <button type="submit" disabled={draft.trim().length === 0}>
            Send
          </button>
        )}
      </form>
    </aside>
  );
}
