import { createShapeId, toRichText, type Editor, type TLShapeId } from "tldraw";
import type { ToolCall, ToolResult } from "../agent/protocol";
import {
  DEFAULT_SHAPE,
  placeInViewport,
  placeRelative,
  type Direction,
} from "./layout";

/**
 * Colour applied to everything the agent draws, so you can always tell who drew
 * what (plan.md §2.5). Small feature, large trust payoff.
 */
export const AGENT_COLOR = "violet";

/**
 * The prop a shape labels with, which differs by shape type.
 *
 * Arrows take a plain `text` string; geo and text shapes take `richText`. Using
 * the wrong one throws a ValidationError from inside editor.run() — "At
 * shape(type = arrow).props.richText: Unexpected property" — which takes the
 * whole tldraw store down and shows the user a crash screen rather than a failed
 * tool call.
 */
function labelProps(shapeType: string, text: string): Record<string, unknown> {
  return shapeType === "arrow" ? { text } : { richText: toRichText(text) };
}

const GEO_FOR_SHAPE: Record<string, string> = {
  box: "rectangle",
  ellipse: "ellipse",
  // A flow chart needs a decision node. Without one the model has nothing that
  // fits an if/else, and it would rather claim it drew a diamond than call a
  // tool that cannot make one.
  diamond: "diamond",
};

interface CreateShapeArgs {
  shape?: string;
  text?: string;
  near?: string;
  direction?: Direction;
}

interface CreateArrowArgs {
  from_id?: string;
  to_id?: string;
  text?: string;
}

interface UpdateShapeArgs {
  id?: string;
  text?: string;
  color?: string;
}

interface DeleteShapeArgs {
  id?: string;
}

/**
 * Applies one agent tool call to the tldraw store.
 *
 * Each call runs inside a single `editor.run()` so one Cmd+Z undoes exactly one
 * agent action (D5). Failures return an error result rather than throwing: the
 * model sees them and can adapt.
 */
export function executeToolCall(editor: Editor, call: ToolCall): ToolResult {
  try {
    switch (call.name) {
      case "create_shape":
        return createShape(editor, call);
      case "create_arrow":
        return createArrow(editor, call);
      case "update_shape":
        return updateShape(editor, call);
      case "delete_shape":
        return deleteShape(editor, call);
      default:
        return fail(call, `unknown tool: ${call.name}`);
    }
  } catch (err) {
    return fail(call, err instanceof Error ? err.message : String(err));
  }
}

function createShape(editor: Editor, call: ToolCall): ToolResult {
  const args = call.args as CreateShapeArgs;
  if (!args.text) return fail(call, "text is required");

  const id = createShapeId();
  const anchor = args.near
    ? editor.getShape(args.near as TLShapeId)
    : undefined;
  if (args.near && !anchor) {
    return fail(call, `no shape with id ${args.near}`);
  }

  const position =
    anchor && args.direction
      ? placeRelative(editor, anchor, args.direction)
      : placeInViewport(editor);

  editor.run(() => {
    if (args.shape === "text") {
      editor.createShape({
        id,
        type: "text",
        x: position.x,
        y: position.y,
        props: { richText: toRichText(args.text!), color: AGENT_COLOR },
      });
      return;
    }
    editor.createShape({
      id,
      type: "geo",
      x: position.x,
      y: position.y,
      props: {
        geo: GEO_FOR_SHAPE[args.shape ?? "box"] ?? "rectangle",
        ...DEFAULT_SHAPE,
        richText: toRichText(args.text!),
        color: AGENT_COLOR,
      },
    });
  });

  return { id: call.id, ok: true, resulting_shape_ids: [id] };
}

function createArrow(editor: Editor, call: ToolCall): ToolResult {
  const args = call.args as CreateArrowArgs;
  if (!args.from_id || !args.to_id)
    return fail(call, "from_id and to_id are required");

  const from = editor.getShape(args.from_id as TLShapeId);
  const to = editor.getShape(args.to_id as TLShapeId);
  if (!from) return fail(call, `no shape with id ${args.from_id}`);
  if (!to) return fail(call, `no shape with id ${args.to_id}`);

  const id = createShapeId();
  editor.run(() => {
    editor.createShape({
      id,
      type: "arrow",
      props: {
        color: AGENT_COLOR,
        // Arrows label with a PLAIN `text` string. richText is a geo-shape prop:
        // setting it here throws "At shape(type = arrow).props.richText:
        // Unexpected property" from inside editor.run(), which takes the whole
        // tldraw store down and shows the user a crash screen.
        ...(args.text ? labelProps("arrow", args.text) : {}),
      },
    });
    // Bindings are what make the arrow follow its shapes — and what the
    // serializer reads back as structure.
    for (const [terminal, target] of [
      ["start", args.from_id],
      ["end", args.to_id],
    ] as const) {
      editor.createBinding({
        type: "arrow",
        fromId: id,
        toId: target as TLShapeId,
        props: {
          terminal,
          normalizedAnchor: { x: 0.5, y: 0.5 },
          isExact: false,
          isPrecise: false,
        },
      });
    }
  });

  return { id: call.id, ok: true, resulting_shape_ids: [id] };
}

function updateShape(editor: Editor, call: ToolCall): ToolResult {
  const args = call.args as UpdateShapeArgs;
  if (!args.id) return fail(call, "id is required");

  const shape = editor.getShape(args.id as TLShapeId);
  if (!shape) return fail(call, `no shape with id ${args.id}`);

  editor.run(() => {
    const props: Record<string, unknown> = {};
    if (args.text !== undefined) {
      Object.assign(props, labelProps(shape.type, args.text));
    }
    if (args.color !== undefined) props.color = args.color;
    editor.updateShape({ id: shape.id, type: shape.type, props });
  });

  return { id: call.id, ok: true, resulting_shape_ids: [shape.id] };
}

function deleteShape(editor: Editor, call: ToolCall): ToolResult {
  const args = call.args as DeleteShapeArgs;
  if (!args.id) return fail(call, "id is required");

  const shape = editor.getShape(args.id as TLShapeId);
  if (!shape) return fail(call, `no shape with id ${args.id}`);

  editor.run(() => editor.deleteShape(shape.id));
  return { id: call.id, ok: true, resulting_shape_ids: [] };
}

function fail(call: ToolCall, error: string): ToolResult {
  return { id: call.id, ok: false, error };
}
