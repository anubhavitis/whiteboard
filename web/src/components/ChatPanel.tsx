import { useEffect, useRef, useState } from "react";
import type { ChatMessage } from "../agent/useChat";
import type { ConnectionStatus } from "../agent/protocol";
import { ConnectionDot } from "./ConnectionDot";

interface Props {
  messages: ChatMessage[];
  streaming: boolean;
  error: string | null;
  status: ConnectionStatus;
  agents: string[];
  agent: string | null;
  onSend: (text: string) => void;
  onCancel: () => void;
  onSwitchAgent: (name: string) => void;
}

/** What each agent is, for the dropdown. Unknown names fall back to the name. */
const AGENT_LABELS: Record<string, string> = {
  "claude-code": "Claude Code — can draw",
  local: "Local MLX — chat only",
  echo: "Echo — offline test",
};

export function ChatPanel({
  messages,
  streaming,
  error,
  status,
  agents,
  agent,
  onSend,
  onCancel,
  onSwitchAgent,
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

      {agents.length > 1 && (
        <div className="chat__agent">
          <label htmlFor="agent-select">Agent</label>
          <select
            id="agent-select"
            value={agent ?? ""}
            onChange={(e) => onSwitchAgent(e.target.value)}
            // Switching mid-turn would orphan the streaming reply.
            disabled={streaming}
            title={streaming ? "Finish or stop the turn first" : undefined}
          >
            {agents.map((name) => (
              <option key={name} value={name}>
                {AGENT_LABELS[name] ?? name}
              </option>
            ))}
          </select>
        </div>
      )}

      <div className="chat__messages" ref={listRef}>
        {messages.length === 0 && (
          <p className="chat__empty">
            Draw something, then ask what you're missing.
          </p>
        )}
        {messages.map((message) => (
          <div
            key={message.id}
            className={`chat__message chat__message--${message.role}`}
          >
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
