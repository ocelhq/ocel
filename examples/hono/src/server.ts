import { serve } from "@hono/node-server";
import { Hono } from "hono";
import { createRouteHandler } from "ocel/blob/hono";
import { pg, uploads } from "../ocel/index";

const app = new Hono();

const PORT = Number(process.env.PORT ?? 3103);

app.get("/api/health", (c) => c.json({ ok: true }));

app.post("/api/todos", async (c) => {
  const body = (await c.req.json().catch(() => null)) as {
    title?: unknown;
  } | null;
  if (!body || typeof body.title !== "string" || body.title.length === 0) {
    return c.json({ error: "title is required" }, 400);
  }
  const { rows } = await pg.query(
    "INSERT INTO todos (title) VALUES ($1) RETURNING id, title, done",
    [body.title],
  );
  return c.json(rows[0], 201);
});

app.get("/api/todos", async (c) => {
  const { rows } = await pg.query(
    "SELECT id, title, done FROM todos ORDER BY id",
  );
  return c.json(rows);
});

app.get("/api/todos/:id", async (c) => {
  const { rows } = await pg.query(
    "SELECT id, title, done FROM todos WHERE id = $1",
    [Number(c.req.param("id"))],
  );
  if (rows.length === 0) {
    return c.json({ error: "not found" }, 404);
  }
  return c.json(rows[0]);
});

app.delete("/api/todos/:id", async (c) => {
  const { rowCount } = await pg.query("DELETE FROM todos WHERE id = $1", [
    Number(c.req.param("id")),
  ]);
  if (!rowCount) {
    return c.json({ error: "not found" }, 404);
  }
  return c.body(null, 204);
});

app.on(["GET", "POST"], "/api/upload", createRouteHandler(uploads));

app.get("/api/documents", async (c) => {
  const { rows } = await pg.query(
    "SELECT id, key, name, mime_type, size, owner_id, thumbnail_key FROM documents ORDER BY id",
  );
  return c.json(rows);
});

serve({ fetch: app.fetch, port: PORT }, () => {
  console.log(`hono example listening on http://localhost:${PORT}`);
});
