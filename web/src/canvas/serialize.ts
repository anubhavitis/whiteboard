import { Box } from "tldraw";
import type { Editor, TLArrowBinding, TLShape } from "tldraw";
import type { CanvasContext, SerializedArrow, SerializedShape } from "./types";

/**
 * Rough token budget for the canvas payload (plan.md §1.5). Canvas JSON bloats
 * faster than intuition suggests — a modest brainstorm runs to hundreds of
 * shapes — so we truncate to viewport-visible shapes rather than let a session
 * silently blow past the context window.
 */
export const MAX_CONTEXT_TOKENS = 8000;

/** JSON averages ~4 characters per token; close enough for a budget check. */
export function estimateTokens(json: string): number {
  return Math.ceil(json.length / 4);
}

function readText(shape: TLShape): string | undefined {
  const text = (shape.props as { text?: unknown }).text;
  return typeof text === "string" && text.length > 0 ? text : undefined;
}

function readSize(shape: TLShape): { w: number; h: number } {
  const props = shape.props as { w?: unknown; h?: unknown };
  return {
    w: Math.round(typeof props.w === "number" ? props.w : 0),
    h: Math.round(typeof props.h === "number" ? props.h : 0),
  };
}

/**
 * Walks the current page and emits the model-facing canvas format.
 *
 * Shapes are ordered by the editor's own sort order so the payload is stable
 * across calls — an unstable payload would defeat prompt caching later.
 */
export function serializeCanvas(editor: Editor): CanvasContext {
  const all = editor.getCurrentPageShapesSorted();
  const selected = new Set(editor.getSelectedShapeIds());

  const shapes: SerializedShape[] = [];
  const arrows: SerializedArrow[] = [];

  for (const shape of all) {
    if (shape.type === "arrow") {
      arrows.push(serializeArrow(editor, shape));
      continue;
    }
    const { w, h } = readSize(shape);
    const entry: SerializedShape = {
      id: shape.id,
      type: shape.type,
      x: Math.round(shape.x),
      y: Math.round(shape.y),
      w,
      h,
    };
    const text = readText(shape);
    if (text) entry.text = text;
    if (selected.has(shape.id)) entry.selected = true;
    shapes.push(entry);
  }

  const context: CanvasContext = {
    shapes,
    arrows,
    truncated: false,
    totalShapes: shapes.length,
  };

  return truncateToViewport(editor, context);
}

function serializeArrow(editor: Editor, shape: TLShape): SerializedArrow {
  const bindings = editor.getBindingsFromShape<TLArrowBinding>(shape, "arrow");
  const arrow: SerializedArrow = {
    id: shape.id,
    from: bindings.find((b) => b.props.terminal === "start")?.toId ?? null,
    to: bindings.find((b) => b.props.terminal === "end")?.toId ?? null,
  };
  const text = readText(shape);
  if (text) arrow.text = text;
  return arrow;
}

/**
 * Drops off-screen shapes when the payload exceeds the token budget, keeping
 * the arrows whose endpoints both survive. Selected shapes are always kept:
 * they're what the user is asking about (plan.md §3.1).
 */
function truncateToViewport(
  editor: Editor,
  context: CanvasContext,
): CanvasContext {
  if (estimateTokens(JSON.stringify(context)) <= MAX_CONTEXT_TOKENS) {
    return context;
  }

  const viewport = editor.getViewportPageBounds();
  const kept = context.shapes.filter((shape) => {
    if (shape.selected) return true;
    return viewport.collides(
      Box.From({ x: shape.x, y: shape.y, w: shape.w, h: shape.h }),
    );
  });

  const keptIds = new Set(kept.map((s) => s.id));
  return {
    shapes: kept,
    arrows: context.arrows.filter(
      (a) =>
        (a.from === null || keptIds.has(a.from)) &&
        (a.to === null || keptIds.has(a.to)),
    ),
    truncated: kept.length < context.shapes.length,
    totalShapes: context.totalShapes,
  };
}
