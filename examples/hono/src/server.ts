import { serve } from "@hono/node-server";
import { Hono } from "hono";
import { createRouteHandler } from "ocel/blob/hono";
import { migrate, pg, uploads } from "../ocel/index";

const app = new Hono();

const PORT = Number(process.env.PORT ?? 3103);

app.get("/api/health", (c) => c.json({ ok: true }));

app.get("/api/status/:code", (c) =>
  c.json({ framework: "hono" }, Number(c.req.param("code")) as 200),
);

app.all("/api/echo/*", async (c) => {
  const url = new URL(c.req.url);
  return c.json({
    framework: "hono",
    method: c.req.method,
    path: url.pathname,
    query: Object.fromEntries(url.searchParams),
    probeHeader: c.req.header("x-ocel-probe") ?? null,
    body: c.req.header("content-type")?.includes("application/json")
      ? await c.req.json()
      : null,
  });
});

app.post("/api/todos", async (c) => {
  await migrate();
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

app.put("/api/todos/:id", async (c) => {
  const body = (await c.req.json().catch(() => null)) as {
    title?: unknown;
    done?: unknown;
  } | null;
  if (
    !body ||
    typeof body.title !== "string" ||
    body.title.length === 0 ||
    typeof body.done !== "boolean"
  ) {
    return c.json({ error: "title and done are required" }, 400);
  }
  const { rows } = await pg.query(
    "UPDATE todos SET title = $1, done = $2 WHERE id = $3 RETURNING id, title, done",
    [body.title, body.done, Number(c.req.param("id"))],
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
