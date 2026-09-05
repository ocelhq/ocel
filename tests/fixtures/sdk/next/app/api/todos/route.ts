import { createTodo, listTodos } from "../../../lib/todos";

export const runtime = "nodejs";

export async function GET() {
  return Response.json(await listTodos());
}

export async function POST(request: Request) {
  const body = (await request.json().catch(() => null)) as {
    title?: unknown;
  } | null;
  if (!body || typeof body.title !== "string" || body.title.length === 0) {
    return Response.json({ error: "title is required" }, { status: 400 });
  }
  return Response.json(await createTodo(body.title), { status: 201 });
}
