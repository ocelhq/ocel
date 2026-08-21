import Fastify from "fastify";
import { createRouteHandler } from "ocel/blob";
import { pg, uploads } from "../ocel/index";

const app = Fastify({ logger: true });
const PORT = Number(process.env.PORT ?? 3104);
const uploadHandlers = createRouteHandler(uploads);

app.get("/api/health", async () => ({ ok: true }));

app.post<{ Body: { title?: unknown } }>("/api/todos", async (request, reply) => {
  const { title } = request.body ?? {};
  if (typeof title !== "string" || title.length === 0) {
    return reply.status(400).send({ error: "title is required" });
  }
  const { rows } = await pg.query(
    "INSERT INTO todos (title) VALUES ($1) RETURNING id, title, done",
    [title],
  );
  return reply.status(201).send(rows[0]);
});

app.get("/api/todos", async () => {
  const { rows } = await pg.query(
    "SELECT id, title, done FROM todos ORDER BY id",
  );
  return rows;
});

app.get<{ Params: { id: string } }>("/api/todos/:id", async (request, reply) => {
  const { rows } = await pg.query(
    "SELECT id, title, done FROM todos WHERE id = $1",
    [Number(request.params.id)],
  );
  if (rows.length === 0) {
    return reply.status(404).send({ error: "not found" });
  }
  return rows[0];
});

app.delete<{ Params: { id: string } }>(
  "/api/todos/:id",
  async (request, reply) => {
    const { rowCount } = await pg.query("DELETE FROM todos WHERE id = $1", [
      Number(request.params.id),
    ]);
    if (!rowCount) {
      return reply.status(404).send({ error: "not found" });
    }
    return reply.status(204).send();
  },
);

app.route({
  method: ["GET", "POST"],
  url: "/api/upload",
  handler: async (request, reply) => {
    Object.assign(request.raw, { body: request.body });
    const handler =
      request.method === "GET" ? uploadHandlers.GET : uploadHandlers.POST;
    const response = await handler(request.raw, request);
    reply.status(response.status);
    response.headers.forEach((value, key) => reply.header(key, value));
    return reply.send(Buffer.from(await response.arrayBuffer()));
  },
});

app.get("/api/documents", async () => {
  const { rows } = await pg.query(
    "SELECT id, key, name, mime_type, size, owner_id, thumbnail_key FROM documents ORDER BY id",
  );
  return rows;
});

await app.listen({ port: PORT });
