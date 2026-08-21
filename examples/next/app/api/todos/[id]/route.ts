import { deleteTodo, getTodo, updateTodo } from "../../../../lib/todos";

export async function GET(
  _request: Request,
  { params }: { params: Promise<{ id: string }> },
) {
  const { id } = await params;
  const todo = await getTodo(Number(id));
  if (!todo) {
    return Response.json({ error: "not found" }, { status: 404 });
  }
  return Response.json(todo);
}

export async function DELETE(
  _request: Request,
  { params }: { params: Promise<{ id: string }> },
) {
  const { id } = await params;
  const deleted = await deleteTodo(Number(id));
  if (!deleted) {
    return Response.json({ error: "not found" }, { status: 404 });
  }
  return new Response(null, { status: 204 });
}

export async function PUT(
  request: Request,
  { params }: { params: Promise<{ id: string }> },
) {
  const body = (await request.json().catch(() => null)) as {
    title?: unknown;
    done?: unknown;
  } | null;
  if (
    !body ||
    typeof body.title !== "string" ||
    body.title.length === 0 ||
    typeof body.done !== "boolean"
  ) {
    return Response.json(
      { error: "title and done are required" },
      { status: 400 },
    );
  }
  const { id } = await params;
  const todo = await updateTodo(Number(id), body.title, body.done);
  if (!todo) {
    return Response.json({ error: "not found" }, { status: 404 });
  }
  return Response.json(todo);
}
