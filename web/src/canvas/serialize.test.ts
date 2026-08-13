import { describe, expect, it } from "vitest";
import { Box, type Editor } from "tldraw";
import { estimateTokens, MAX_CONTEXT_TOKENS, serializeCanvas } from "./serialize";

/**
 * A minimal stand-in for the parts of the editor the serializer reads. Driving
 * a real tldraw editor needs a DOM and a mounted component; the serializer only
 * touches four methods, so a fake keeps these tests fast and deterministic.
 */
function fakeEditor(options: {
  shapes: Array<Record<string, unknown>>;
  bindings?: Record<string, Array<{ toId: string; props: { terminal: "start" | "end" } }>>;
  selected?: string[];
  viewport?: { x: number; y: number; w: number; h: number };
}): Editor {
  const { shapes, bindings = {}, selected = [], viewport } = options;
  return {
    getCurrentPageShapesSorted: () => shapes,
    getSelectedShapeIds: () => selected,
    getBindingsFromShape: (shape: { id: string }) => bindings[shape.id] ?? [],
    getViewportPageBounds: () =>
      Box.From(viewport ?? { x: -10000, y: -10000, w: 20000, h: 20000 }),
  } as unknown as Editor;
}

/**
 * A shape whose label lives in richText, which is what tldraw actually stores
 * and what execute.ts writes. The earlier fixtures used props.text — a shape the
 * real app never produces — which is exactly why a serializer that only read
 * props.text passed its tests while sending the model unlabeled shapes.
 */
const richBox = (id: string, text: string, x = 0, y = 0) => ({
  id,
  type: "geo",
  x,
  y,
  props: {
    w: 100,
    h: 50,
    richText: {
      type: "doc",
      content: [
        { type: "paragraph", content: [{ type: "text", text }] },
      ],
    },
  },
});

const box = (id: string, text: string, x = 0, y = 0) => ({
  id,
  type: "geo",
  x,
  y,
  props: { text, w: 100, h: 50 },
});

describe("serializeCanvas", () => {
  it("emits shapes with text, rounded position, and size", () => {
    const editor = fakeEditor({
      shapes: [{ ...box("shape:a", "Frontend"), x: 12.7, y: 40.2 }],
    });

    const result = serializeCanvas(editor);

    expect(result.shapes).toEqual([
      { id: "shape:a", type: "geo", x: 13, y: 40, w: 100, h: 50, text: "Frontend" },
    ]);
    expect(result.arrows).toEqual([]);
    expect(result.truncated).toBe(false);
  });

  // Structure is what the model reasons about well, so bindings must survive
  // serialization intact — this is the highest-value assertion in the file.
  it("resolves arrow bindings to from/to shape ids", () => {
    const editor = fakeEditor({
      shapes: [
        box("shape:a", "Frontend"),
        box("shape:b", "Backend", 300, 0),
        { id: "shape:arrow", type: "arrow", x: 100, y: 25, props: { text: "calls" } },
      ],
      bindings: {
        "shape:arrow": [
          { toId: "shape:a", props: { terminal: "start" } },
          { toId: "shape:b", props: { terminal: "end" } },
        ],
      },
    });

    const result = serializeCanvas(editor);

    expect(result.shapes.map((s) => s.id)).toEqual(["shape:a", "shape:b"]);
    expect(result.arrows).toEqual([
      { id: "shape:arrow", from: "shape:a", to: "shape:b", text: "calls" },
    ]);
  });

  it("represents an unbound arrow terminal as null", () => {
    const editor = fakeEditor({
      shapes: [box("shape:a", "Frontend"), { id: "shape:arrow", type: "arrow", x: 0, y: 0, props: {} }],
      bindings: {
        "shape:arrow": [{ toId: "shape:a", props: { terminal: "start" } }],
      },
    });

    expect(serializeCanvas(editor).arrows).toEqual([
      { id: "shape:arrow", from: "shape:a", to: null },
    ]);
  });

  it("marks selected shapes and omits the flag otherwise", () => {
    const editor = fakeEditor({
      shapes: [box("shape:a", "A"), box("shape:b", "B", 200, 0)],
      selected: ["shape:b"],
    });

    const [a, b] = serializeCanvas(editor).shapes;
    expect(a.selected).toBeUndefined();
    expect(b.selected).toBe(true);
  });

  it("omits text when a shape has none", () => {
    const editor = fakeEditor({
      shapes: [{ id: "shape:a", type: "geo", x: 0, y: 0, props: { text: "", w: 10, h: 10 } }],
    });

    expect(serializeCanvas(editor).shapes[0]).not.toHaveProperty("text");
  });

  describe("token guard", () => {
    // Enough labelled shapes to blow the budget, spread far apart so most fall
    // outside the viewport.
    const many = Array.from({ length: 400 }, (_, i) =>
      box(`shape:${i}`, `Component number ${i} with a reasonably long label`, i * 500, 0),
    );

    it("truncates to viewport-visible shapes when over budget", () => {
      const editor = fakeEditor({
        shapes: many,
        viewport: { x: -100, y: -100, w: 1200, h: 400 },
      });

      const result = serializeCanvas(editor);

      expect(result.truncated).toBe(true);
      expect(result.totalShapes).toBe(400);
      expect(result.shapes.length).toBeLessThan(400);
      expect(estimateTokens(JSON.stringify(result))).toBeLessThanOrEqual(MAX_CONTEXT_TOKENS);
    });

    it("keeps selected shapes even when they are off-screen", () => {
      const editor = fakeEditor({
        shapes: many,
        selected: ["shape:399"],
        viewport: { x: -100, y: -100, w: 1200, h: 400 },
      });

      const result = serializeCanvas(editor);

      expect(result.shapes.some((s) => s.id === "shape:399")).toBe(true);
    });

    it("drops arrows whose endpoints were truncated away", () => {
      const editor = fakeEditor({
        shapes: [
          ...many,
          { id: "shape:arrow", type: "arrow", x: 0, y: 0, props: {} },
        ],
        bindings: {
          // shape:399 sits far off-screen and is not selected, so it is dropped.
          "shape:arrow": [
            { toId: "shape:0", props: { terminal: "start" } },
            { toId: "shape:399", props: { terminal: "end" } },
          ],
        },
        viewport: { x: -100, y: -100, w: 1200, h: 400 },
      });

      expect(serializeCanvas(editor).arrows).toEqual([]);
    });

    it("leaves a small canvas untruncated", () => {
      const editor = fakeEditor({
        shapes: [box("shape:a", "A"), box("shape:b", "B", 200, 0)],
        viewport: { x: 0, y: 0, w: 10, h: 10 },
      });

      const result = serializeCanvas(editor);
      expect(result.truncated).toBe(false);
      expect(result.shapes).toHaveLength(2);
    });
  });
});

describe("labels stored as richText", () => {
  // Regression: tldraw stores labels in props.richText, and execute.ts writes
  // them that way. A serializer reading only props.text sent the model shapes
  // with no labels at all, so the agent quoted opaque ids back at the user.
  it("reads a label out of richText", () => {
    const editor = fakeEditor({ shapes: [richBox("shape:a", "Auth Service")] });
    expect(serializeCanvas(editor).shapes[0].text).toBe("Auth Service");
  });

  it("still reads a plain props.text label", () => {
    const editor = fakeEditor({ shapes: [box("shape:a", "Frontend")] });
    expect(serializeCanvas(editor).shapes[0].text).toBe("Frontend");
  });

  it("marks a shape with no label as unlabeled", () => {
    // Without this the model reads a missing text field as licence to use the id
    // as a name and reports a box "labeled shape:HAxxVP1i".
    const editor = fakeEditor({
      shapes: [{ id: "shape:a", type: "geo", x: 0, y: 0, props: { w: 10, h: 10 } }],
    });
    const shape = serializeCanvas(editor).shapes[0];
    expect(shape.unlabeled).toBe(true);
    expect(shape).not.toHaveProperty("text");
  });

  it("does not mark a labelled shape as unlabeled", () => {
    const editor = fakeEditor({ shapes: [richBox("shape:a", "Auth")] });
    expect(serializeCanvas(editor).shapes[0]).not.toHaveProperty("unlabeled");
  });

  it("omits the label when richText is empty", () => {
    const editor = fakeEditor({
      shapes: [
        {
          id: "shape:a",
          type: "geo",
          x: 0,
          y: 0,
          props: { w: 10, h: 10, richText: { type: "doc", content: [{ type: "paragraph" }] } },
        },
      ],
    });
    expect(serializeCanvas(editor).shapes[0]).not.toHaveProperty("text");
  });
});
