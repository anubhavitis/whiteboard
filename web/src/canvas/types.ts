/**
 * The canvas context format sent to the model.
 *
 * Coordinates are rounded and secondary; the model is told to reason about
 * structure via arrow bindings (`from`/`to`), which it does far better than raw
 * geometry. See DECISIONS.md and plan.md §1.1.
 */
export interface CanvasContext {
  shapes: SerializedShape[];
  arrows: SerializedArrow[];
  /** True when shapes were dropped to stay under the token budget. */
  truncated: boolean;
  /** Total shapes on the page, before any truncation. */
  totalShapes: number;
}

export interface SerializedShape {
  id: string;
  type: string;
  text?: string;
  x: number;
  y: number;
  w: number;
  h: number;
  /** Present only when true, to keep the payload small. */
  selected?: boolean;
  /**
   * Present only when the shape has no label at all. Without it the model reads
   * the absence of `text` as an invitation to use the id as a name, and tells the
   * user about a box "labeled shape:HAxxVP1i".
   */
  unlabeled?: true;
}

export interface SerializedArrow {
  id: string;
  text?: string;
  /** Shape id the arrow starts at, or null when the end is unbound. */
  from: string | null;
  to: string | null;
}
