import { Box, type Editor, type TLShape } from "tldraw";

export type Direction = "above" | "below" | "left_of" | "right_of";

export const DEFAULT_SHAPE = { w: 160, h: 90 };

/** Space left between a new shape and its anchor. */
const GAP = 70;

/** How far to step when nudging out of a collision. */
const NUDGE = 30;

/** Give up nudging rather than loop forever on a dense canvas. */
const MAX_NUDGES = 40;

export interface Placement {
  x: number;
  y: number;
}

function shapeBounds(editor: Editor, shape: TLShape): Box {
  return editor.getShapePageBounds(shape) ?? Box.From({ x: shape.x, y: shape.y, ...DEFAULT_SHAPE });
}

/**
 * Computes pixel coordinates for a shape placed relative to an anchor.
 *
 * This function is the whole reason the model never emits x/y (see
 * DECISIONS.md): relative intent in, collision-free pixels out.
 */
export function placeRelative(
  editor: Editor,
  anchor: TLShape,
  direction: Direction,
  size: { w: number; h: number } = DEFAULT_SHAPE,
): Placement {
  const bounds = shapeBounds(editor, anchor);

  // Centre the new shape on the anchor's axis, then offset past its edge.
  let x: number;
  let y: number;
  switch (direction) {
    case "above":
      x = bounds.x + (bounds.w - size.w) / 2;
      y = bounds.y - size.h - GAP;
      break;
    case "below":
      x = bounds.x + (bounds.w - size.w) / 2;
      y = bounds.y + bounds.h + GAP;
      break;
    case "left_of":
      x = bounds.x - size.w - GAP;
      y = bounds.y + (bounds.h - size.h) / 2;
      break;
    case "right_of":
      x = bounds.x + bounds.w + GAP;
      y = bounds.y + (bounds.h - size.h) / 2;
      break;
  }

  return avoidCollisions(editor, { x, y }, size, direction);
}

/**
 * Steps the placement further along its own axis until it stops overlapping
 * existing shapes. Overlapping output is the visible symptom of bad canvas-agent
 * layout, so it's cheaper to fix here than to prompt around.
 */
function avoidCollisions(
  editor: Editor,
  start: Placement,
  size: { w: number; h: number },
  direction: Direction,
): Placement {
  const existing = editor
    .getCurrentPageShapes()
    .map((shape) => editor.getShapePageBounds(shape))
    .filter((b): b is Box => b !== undefined);

  const step = {
    above: { x: 0, y: -NUDGE },
    below: { x: 0, y: NUDGE },
    left_of: { x: -NUDGE, y: 0 },
    right_of: { x: NUDGE, y: 0 },
  }[direction];

  let { x, y } = start;
  for (let i = 0; i < MAX_NUDGES; i++) {
    const candidate = Box.From({ x, y, w: size.w, h: size.h });
    if (!existing.some((b) => b.collides(candidate))) break;
    x += step.x;
    y += step.y;
  }
  return { x, y };
}

/** Placement for the first shape on an empty canvas: centre of the viewport. */
export function placeInViewport(
  editor: Editor,
  size: { w: number; h: number } = DEFAULT_SHAPE,
): Placement {
  const viewport = editor.getViewportPageBounds();
  return {
    x: viewport.x + (viewport.w - size.w) / 2,
    y: viewport.y + (viewport.h - size.h) / 2,
  };
}
