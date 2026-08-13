import { useRef } from "react";
import { Tldraw, type Editor } from "tldraw";
import "tldraw/tldraw.css";
import type { ServerMessage } from "./agent/protocol";
import { useAgentSocket } from "./agent/useAgentSocket";
import { useChat } from "./agent/useChat";
import { ChatPanel } from "./components/ChatPanel";
import "./App.css";

export default function App() {
  // Held in a ref rather than state: the chat reads the editor on demand when a
  // message is sent, and storing it in state would re-render on every mount.
  const editorRef = useRef<Editor | null>(null);

  // useAgentSocket stores the handler in a ref internally, so passing a
  // callback defined after it would still be seen — but declaring the socket
  // first keeps `send` available to useChat without a second indirection.
  const handlerRef = useRef<(msg: ServerMessage) => void>(() => {});
  const { status, send } = useAgentSocket({
    onMessage: (msg) => handlerRef.current(msg),
  });

  const {
    messages,
    streaming,
    error,
    agents,
    agent,
    skills,
    sendMessage,
    cancel,
    switchAgent,
    setEnabledSkills,
    saveSkill,
    deleteSkill,
    handleServerMessage,
  } = useChat({
    send,
    editorRef,
  });
  handlerRef.current = handleServerMessage;

  return (
    <div className="app">
      <div className="app__canvas">
        <Tldraw
          persistenceKey="whiteboard-partner"
          onMount={(editor) => {
            editorRef.current = editor;
            // Grid on by default. tldraw treats isGridMode as temporary state and
            // does not persist it, so it has to be set on every mount rather than
            // once — and a user who turns it off will see it back next reload.
            editor.updateInstanceState({ isGridMode: true });
          }}
        />
      </div>
      <ChatPanel
        messages={messages}
        streaming={streaming}
        error={error}
        status={status}
        agents={agents}
        agent={agent}
        skills={skills}
        onSend={sendMessage}
        onCancel={cancel}
        onSwitchAgent={switchAgent}
        onSetSkills={setEnabledSkills}
        onSaveSkill={saveSkill}
        onDeleteSkill={deleteSkill}
      />
    </div>
  );
}
