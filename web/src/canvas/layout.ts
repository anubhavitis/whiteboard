import { Box, type Editor, type TLShape } from "tldraw";

export type Direction = "above" | "below" | "left_of" | "right_of";

export const DEFAULT_SHAPE = { w: 160, h: 90 };

/**
 * Size overrides per shape kind.
 *
 * A diamond only has its full width at the vertical midline, so a label that
 * fits a 160px box overflows a 160px diamond. Decision nodes also tend to hold a
 * question rather than two words. Sized generously on both axes.
 */
export const SIZE_FOR_SHAPE: Record<string, { w: number; h: number }> = {
  diamond: { w: 240, h: 140 },
};

/** sizeFor returns the box a shape kind should be drawn at. */
export function sizeFor(shape: string | undefined): { w: number; h: number } {
  return SIZE_FOR_SHAPE[shape ?? ""] ?? DEFAULT_SHAPE;
}

/**
 * Space left between a new shape and its anchor.
 *
 * Sized for a *labelled* arrow, not a bare one. An arrow's label renders at the
 * midpoint of the connector, so at the old 70px the "yes" on a decision branch
 * drew on top of both shapes it sat between. Connectors also read as connectors
 * only when they are visibly longer than the gap between two touching boxes.
 */
const GAP = 140;

/** How far to step when nudging out of a collision. */
const NUDGE = 30;

/** Give up nudging rather than loop forever on a dense canvas. */
const MAX_NUDGES = 40;

export interface Placement {
  x: number;
  y: number;
}

function shapeBounds(editor: Editor, shape: TLShape): Box {
  return (
    editor.getShapePageBounds(shape) ??
    Box.From({ x: shape.x, y: shape.y, ...DEFAULT_SHAPE })
  );
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

  // Pad the candidate so a shape does not merely stop overlapping but keeps
  // breathing room — two boxes sharing an edge still read as one blob, and an
  // arrow between them has nowhere to draw its label.
  const pad = NUDGE;

  let { x, y } = start;
  for (let i = 0; i < MAX_NUDGES; i++) {
    const candidate = Box.From({
      x: x - pad,
      y: y - pad,
      w: size.w + pad * 2,
      h: size.h + pad * 2,
    });
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
