import { serve } from "@hono/node-server";
import { Hono } from "hono";
import { createRouteHandler } from "ocel/blob/hono";
import { pg, uploads } from "../ocel/index";

// postgres("main") is resolved from the environment `ocel dev` injects, so the
// server never sees a connection string of its own.
const app = new Hono();

const PORT = Number(process.env.PORT ?? 3103);

// Readiness probe the e2e harness polls before hitting the CRUD routes.
app.get("/health", (c) => c.json({ ok: true }));

app.post("/todos", async (c) => {
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

app.get("/todos", async (c) => {
  const { rows } = await pg.query(
    "SELECT id, title, done FROM todos ORDER BY id",
  );
  return c.json(rows);
});

app.get("/todos/:id", async (c) => {
  const { rows } = await pg.query(
    "SELECT id, title, done FROM todos WHERE id = $1",
    [Number(c.req.param("id"))],
  );
  if (rows.length === 0) {
    return c.json({ error: "not found" }, 404);
  }
  return c.json(rows[0]);
});

app.delete("/todos/:id", async (c) => {
  const { rowCount } = await pg.query("DELETE FROM todos WHERE id = $1", [
    Number(c.req.param("id")),
  ]);
  if (!rowCount) {
    return c.json({ error: "not found" }, 404);
  }
  return c.body(null, 204);
});

// The upload surface for the `uploads` bucket (?op=presign|callback|poll),
// mounted for both methods at this exact path.
app.on(["GET", "POST"], "/api/upload", createRouteHandler(uploads));

app.get("/documents", async (c) => {
  const { rows } = await pg.query(
    "SELECT id, key, name, mime_type, size, owner_id FROM documents ORDER BY id",
  );
  return c.json(rows);
});

serve({ fetch: app.fetch, port: PORT }, () => {
  console.log(`hono example listening on http://localhost:${PORT}`);
});
