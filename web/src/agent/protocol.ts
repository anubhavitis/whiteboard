// Mirror of server/internal/ws/protocol.go. Both sides must change together.

import type { CanvasContext } from "../canvas/types";

export type ClientMessage =
  | {
      type: "user_message";
      payload: { text: string; canvas_context?: CanvasContext };
    }
  | { type: "tool_result"; payload: ToolResult }
  | { type: "switch_agent"; payload: { name: string } }
  | { type: "cancel" }
  | { type: "ping" };

export type ServerMessage =
  | { type: "assistant_delta"; payload: { text: string } }
  | { type: "tool_call"; payload: ToolCall }
  | { type: "turn_end" }
  | { type: "error"; payload: { message: string } }
  | { type: "agents_available"; payload: { names: string[]; current: string } }
  | { type: "agent_switched"; payload: { current: string } }
  | { type: "pong" };

export interface ToolCall {
  id: string;
  name: string;
  args: unknown;
}

export interface ToolResult {
  id: string;
  ok: boolean;
  resulting_shape_ids?: string[];
  error?: string;
}

export type ConnectionStatus = "connecting" | "connected" | "disconnected";
