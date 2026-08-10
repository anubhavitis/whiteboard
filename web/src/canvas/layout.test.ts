import { describe, expect, it } from "vitest";
import { Box, type Editor, type TLShape } from "tldraw";
import { DEFAULT_SHAPE, placeInViewport, placeRelative } from "./layout";

const shape = (id: string, x: number, y: number, w = 160, h = 90) =>
  ({ id, type: "geo", x, y, props: { w, h } }) as unknown as TLShape;

function fakeEditor(shapes: TLShape[], viewport = { x: 0, y: 0, w: 1000, h: 800 }): Editor {
  return {
    getCurrentPageShapes: () => shapes,
    getShapePageBounds: (s: TLShape) => {
      const props = s.props as { w: number; h: number };
      return Box.From({ x: s.x, y: s.y, w: props.w, h: props.h });
    },
    getViewportPageBounds: () => Box.From(viewport),
  } as unknown as Editor;
}

const anchor = shape("shape:anchor", 100, 100);

describe("placeRelative", () => {
  it("places a shape to the right, past the anchor's edge", () => {
    const { x, y } = placeRelative(fakeEditor([anchor]), anchor, "right_of");

    expect(x).toBeGreaterThan(100 + 160); // clear of the anchor
    expect(y).toBe(100); // vertically centred on a same-height anchor
  });

  it("places a shape below, past the anchor's bottom", () => {
    const { x, y } = placeRelative(fakeEditor([anchor]), anchor, "below");

    expect(y).toBeGreaterThan(100 + 90);
    expect(x).toBe(100);
  });

  it("places above and left on the negative side of the anchor", () => {
    const editor = fakeEditor([anchor]);

    expect(placeRelative(editor, anchor, "above").y).toBeLessThan(100);
    expect(placeRelative(editor, anchor, "left_of").x).toBeLessThan(100);
  });

  // The core guarantee: output never overlaps an existing shape. Overlapping
  // shapes are the visible symptom of bad canvas-agent layout.
  it("nudges past an occupied slot instead of overlapping it", () => {
    const occupier = shape("shape:occupier", 330, 100); // sits exactly right_of anchor
    const editor = fakeEditor([anchor, occupier]);

    const placement = placeRelative(editor, anchor, "right_of");
    const placed = Box.From({ ...placement, ...DEFAULT_SHAPE });

    expect(placed.collides(Box.From({ x: 330, y: 100, w: 160, h: 90 }))).toBe(false);
    expect(placed.collides(Box.From({ x: 100, y: 100, w: 160, h: 90 }))).toBe(false);
  });

  it("clears a run of occupied slots along the direction axis", () => {
    const editor = fakeEditor([
      anchor,
      shape("shape:b", 330, 100),
      shape("shape:c", 400, 100),
      shape("shape:d", 470, 100),
    ]);

    const placement = placeRelative(editor, anchor, "right_of");
    const placed = Box.From({ ...placement, ...DEFAULT_SHAPE });

    for (const other of editor.getCurrentPageShapes()) {
      const b = editor.getShapePageBounds(other)!;
      expect(placed.collides(b)).toBe(false);
    }
  });

  it("centres a smaller shape on the anchor's axis", () => {
    const { y } = placeRelative(fakeEditor([anchor]), anchor, "right_of", { w: 100, h: 50 });

    // anchor spans y 100..190 (centre 145); a 50-tall shape centres at 120.
    expect(y).toBe(120);
  });
});

describe("placeInViewport", () => {
  it("centres the first shape in the viewport", () => {
    const editor = fakeEditor([], { x: 0, y: 0, w: 1000, h: 800 });

    expect(placeInViewport(editor)).toEqual({ x: (1000 - 160) / 2, y: (800 - 90) / 2 });
  });

  it("respects a scrolled viewport origin", () => {
    const editor = fakeEditor([], { x: 500, y: 300, w: 1000, h: 800 });

    expect(placeInViewport(editor)).toEqual({ x: 500 + 420, y: 300 + 355 });
  });
});
