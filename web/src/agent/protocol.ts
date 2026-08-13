// Mirror of server/internal/ws/protocol.go. Both sides must change together.

import type { CanvasContext } from "../canvas/types";

export type ClientMessage =
  | {
      type: "user_message";
      payload: { text: string; canvas_context?: CanvasContext };
    }
  | { type: "tool_result"; payload: ToolResult }
  | { type: "switch_agent"; payload: { name: string } }
  | { type: "set_skills"; payload: { enabled: string[] } }
  | { type: "save_skill"; payload: { id: string; body: string } }
  | { type: "delete_skill"; payload: { id: string } }
  | { type: "cancel" }
  | { type: "ping" };

export type ServerMessage =
  | { type: "assistant_delta"; payload: { text: string } }
  | { type: "tool_call"; payload: ToolCall }
  | { type: "turn_end" }
  | { type: "error"; payload: { message: string } }
  | { type: "agents_available"; payload: { names: string[]; current: string } }
  | { type: "agent_switched"; payload: { current: string } }
  | { type: "skills_state"; payload: SkillsState }
  | { type: "pong" };

/** One skill as the picker sees it. Mirrors ws.SkillInfo. */
export interface SkillInfo {
  id: string;
  name: string;
  description: string;
  built_in: boolean;
  tokens: number;
  body?: string;
}

/**
 * The whole picker state, pushed by the server on connect and after any change,
 * so the UI never reconstructs it locally and cannot drift.
 */
export interface SkillsState {
  skills: SkillInfo[];
  enabled: string[];
  /** Cost of the composed prompt, resent on every turn. */
  prompt_tokens: number;
  /** What the canvas itself gets, for comparison. */
  canvas_budget: number;
}

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
