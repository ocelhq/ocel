import { deleteTodo, getTodo } from "../../../../lib/todos";

export const runtime = "nodejs";

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
