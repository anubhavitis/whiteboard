import { describe, expect, it, vi } from "vitest";
import { Box, type Editor, type TLShape, type TLShapeId } from "tldraw";
import { AGENT_COLOR, executeToolCall } from "./execute";
import type { ToolCall } from "../agent/protocol";

const shape = (id: string, x = 100, y = 100) =>
  ({ id, type: "geo", x, y, props: { w: 160, h: 90 } }) as unknown as TLShape;

interface Recorder {
  editor: Editor;
  created: Array<Record<string, unknown>>;
  bindings: Array<Record<string, unknown>>;
  updated: Array<Record<string, unknown>>;
  deleted: string[];
  /** Boxed so increments stay visible to the caller (a plain number would be copied). */
  runCalls: { count: number };
}

function fakeEditor(shapes: TLShape[] = []): Recorder {
  const byId = new Map<string, TLShape>(shapes.map((s) => [s.id as string, s]));
  const rec = {
    created: [] as Array<Record<string, unknown>>,
    bindings: [] as Array<Record<string, unknown>>,
    updated: [] as Array<Record<string, unknown>>,
    deleted: [] as string[],
    runCalls: { count: 0 },
  };

  const editor = {
    getShape: (id: TLShapeId) => byId.get(id as unknown as string),
    getCurrentPageShapes: () => shapes,
    getShapePageBounds: (s: TLShape) => {
      const p = s.props as { w: number; h: number };
      return Box.From({ x: s.x, y: s.y, w: p.w, h: p.h });
    },
    getViewportPageBounds: () => Box.From({ x: 0, y: 0, w: 1000, h: 800 }),
    run: (fn: () => void) => {
      rec.runCalls.count += 1;
      fn();
    },
    createShape: (s: Record<string, unknown>) => rec.created.push(s),
    createBinding: (b: Record<string, unknown>) => rec.bindings.push(b),
    updateShape: (u: Record<string, unknown>) => rec.updated.push(u),
    deleteShape: (id: string) => rec.deleted.push(id),
  } as unknown as Editor;

  return { ...rec, editor };
}

const call = (name: string, args: Record<string, unknown>): ToolCall => ({
  id: "toolu_1",
  name,
  args,
});

describe("create_shape", () => {
  it("creates a labelled shape in the agent colour and returns its id", () => {
    const r = fakeEditor();

    const result = executeToolCall(
      r.editor,
      call("create_shape", { shape: "box", text: "Cache" }),
    );

    expect(result.ok).toBe(true);
    expect(result.resulting_shape_ids).toHaveLength(1);
    expect(r.created[0].type).toBe("geo");
    expect((r.created[0].props as Record<string, unknown>).color).toBe(
      AGENT_COLOR,
    );
  });

  // One editor.run() per tool call is what makes a single Cmd+Z undo exactly
  // one agent action (D5).
  it("wraps the mutation in exactly one editor.run", () => {
    const r = fakeEditor();
    executeToolCall(
      r.editor,
      call("create_shape", { shape: "box", text: "Cache" }),
    );
    expect(r.runCalls.count).toBe(1);
  });

  it("places relative to the anchor when near+direction are given", () => {
    const anchor = shape("shape:a", 100, 100);
    const r = fakeEditor([anchor]);

    executeToolCall(
      r.editor,
      call("create_shape", {
        shape: "box",
        text: "Cache",
        near: "shape:a",
        direction: "right_of",
      }),
    );

    expect(r.created[0].x as number).toBeGreaterThan(100 + 160);
  });

  it("fails cleanly when the anchor id does not exist", () => {
    const r = fakeEditor();

    const result = executeToolCall(
      r.editor,
      call("create_shape", {
        shape: "box",
        text: "Cache",
        near: "shape:missing",
        direction: "below",
      }),
    );

    expect(result.ok).toBe(false);
    expect(result.error).toContain("shape:missing");
    expect(r.created).toHaveLength(0);
  });

  it("requires text", () => {
    const r = fakeEditor();
    expect(
      executeToolCall(r.editor, call("create_shape", { shape: "box" })).ok,
    ).toBe(false);
  });
});

describe("create_arrow", () => {
  it("creates start and end bindings to the named shapes", () => {
    const r = fakeEditor([shape("shape:a", 0, 0), shape("shape:b", 400, 0)]);

    const result = executeToolCall(
      r.editor,
      call("create_arrow", {
        from_id: "shape:a",
        to_id: "shape:b",
        text: "reads",
      }),
    );

    expect(result.ok).toBe(true);
    expect(r.bindings).toHaveLength(2);
    expect(
      r.bindings.map((b) => (b.props as { terminal: string }).terminal).sort(),
    ).toEqual(["end", "start"]);
    expect(r.bindings.map((b) => b.toId)).toEqual(["shape:a", "shape:b"]);
  });

  it("fails when an endpoint is missing, without creating a dangling arrow", () => {
    const r = fakeEditor([shape("shape:a")]);

    const result = executeToolCall(
      r.editor,
      call("create_arrow", { from_id: "shape:a", to_id: "shape:gone" }),
    );

    expect(result.ok).toBe(false);
    expect(r.created).toHaveLength(0);
    expect(r.bindings).toHaveLength(0);
  });
});

describe("update_shape and delete_shape", () => {
  it("updates only the props provided", () => {
    const r = fakeEditor([shape("shape:a")]);

    executeToolCall(
      r.editor,
      call("update_shape", { id: "shape:a", color: "red" }),
    );

    const props = r.updated[0].props as Record<string, unknown>;
    expect(props.color).toBe("red");
    expect(props).not.toHaveProperty("richText");
  });

  it("deletes an existing shape", () => {
    const r = fakeEditor([shape("shape:a")]);

    const result = executeToolCall(
      r.editor,
      call("delete_shape", { id: "shape:a" }),
    );

    expect(result.ok).toBe(true);
    expect(r.deleted).toEqual(["shape:a"]);
  });

  it("fails on a missing shape rather than throwing", () => {
    const r = fakeEditor();
    expect(
      executeToolCall(r.editor, call("delete_shape", { id: "shape:x" })).ok,
    ).toBe(false);
  });
});

describe("failure handling", () => {
  it("reports an unknown tool name", () => {
    const r = fakeEditor();
    const result = executeToolCall(r.editor, call("teleport_shape", {}));
    expect(result.ok).toBe(false);
    expect(result.error).toContain("teleport_shape");
  });

  // A throwing editor must surface as an error result the model can read,
  // never as an exception that kills the turn.
  it("converts a thrown editor error into an error result", () => {
    const r = fakeEditor();
    vi.spyOn(r.editor, "run").mockImplementation(() => {
      throw new Error("store is read-only");
    });

    const result = executeToolCall(
      r.editor,
      call("create_shape", { shape: "box", text: "X" }),
    );

    expect(result.ok).toBe(false);
    expect(result.error).toContain("store is read-only");
  });

  it("echoes the originating tool call id on every result", () => {
    const r = fakeEditor();
    expect(
      executeToolCall(r.editor, call("create_shape", { text: "A" })).id,
    ).toBe("toolu_1");
    expect(executeToolCall(r.editor, call("delete_shape", {})).id).toBe(
      "toolu_1",
    );
  });
});

describe("label props match what each shape type accepts", () => {
  // Regression: arrows were created with props.richText, which tldraw rejects.
  // The ValidationError is thrown inside editor.run(), so it escapes the tool's
  // try/catch and takes the whole store down — the user gets tldraw's crash
  // screen ("At shape(type = arrow).props.richText: Unexpected property")
  // instead of a failed tool call. The fake editor records props without
  // validating them, so this asserts the prop NAMES rather than relying on it.
  it("labels an arrow with plain text, never richText", () => {
    const r = fakeEditor([shape("shape:a", 0, 0), shape("shape:b", 400, 0)]);
    executeToolCall(r.editor, {
      id: "t1",
      name: "create_arrow",
      args: { from_id: "shape:a", to_id: "shape:b", text: "yes" },
    });
    const arrow = r.created.find((c) => c.type === "arrow");
    const props = arrow?.props as Record<string, unknown>;
    expect(props.text).toBe("yes");
    expect(props).not.toHaveProperty("richText");
  });

  it("labels a geo shape with richText, never plain text", () => {
    const r = fakeEditor();
    executeToolCall(r.editor, {
      id: "t1",
      name: "create_shape",
      args: { shape: "box", text: "Postgres" },
    });
    const props = r.created[0].props as Record<string, unknown>;
    expect(props).toHaveProperty("richText");
    expect(props).not.toHaveProperty("text");
  });

  it("relabels an arrow with plain text", () => {
    const arrow = { id: "shape:arr", type: "arrow", x: 0, y: 0, props: {} } as unknown as TLShape;
    const r = fakeEditor([arrow]);
    executeToolCall(r.editor, {
      id: "t1",
      name: "update_shape",
      args: { id: "shape:arr", text: "no" },
    });
    const props = r.updated[0].props as Record<string, unknown>;
    expect(props.text).toBe("no");
    expect(props).not.toHaveProperty("richText");
  });

  it("relabels a geo shape with richText", () => {
    const r = fakeEditor([shape("shape:a")]);
    executeToolCall(r.editor, {
      id: "t1",
      name: "update_shape",
      args: { id: "shape:a", text: "Redis" },
    });
    const props = r.updated[0].props as Record<string, unknown>;
    expect(props).toHaveProperty("richText");
    expect(props).not.toHaveProperty("text");
  });

  it("maps diamond to the tldraw diamond geo", () => {
    const r = fakeEditor();
    executeToolCall(r.editor, {
      id: "t1",
      name: "create_shape",
      args: { shape: "diamond", text: "Is it green tea?" },
    });
    expect((r.created[0].props as { geo: string }).geo).toBe("diamond");
  });
});
