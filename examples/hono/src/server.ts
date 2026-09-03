import { serve } from "@hono/node-server";
import { Hono } from "hono";
import { createRouteHandler } from "ocel/blob/hono";
import { pg, uploads } from "../ocel/index";
import { probes } from "./probes";

const APP_NAME = "web";
const PORT = Number(process.env.PORT ?? 3103);

const OCEL_SVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64" role="img" aria-label="ocel"><rect width="64" height="64" rx="14" fill="#0b0f14"/><circle cx="24" cy="27" r="5" fill="#f2b705"/><circle cx="42" cy="27" r="5" fill="#f2b705"/><path d="M20 42c4 5 20 5 24 0" stroke="#f2b705" stroke-width="4" fill="none" stroke-linecap="round"/></svg>\n`;
const OCEL_SVG_BYTES = Buffer.from(OCEL_SVG, "utf8");

const app = new Hono();

app.get("/health", (c) => c.json({ ok: true, app: APP_NAME }));

app.get("/ocel.svg", (c) => {
  c.header("content-type", "image/svg+xml");
  c.header("content-length", String(OCEL_SVG_BYTES.byteLength));
  return c.body(OCEL_SVG_BYTES);
});

app.route("/api/probes", probes);

app.all("/api/upload", createRouteHandler(uploads));

app.post("/api/todos", async (c) => {
  const body = (await c.req.json().catch(() => null)) as { title?: unknown } | null;
  const title = body?.title;
  if (typeof title !== "string" || title.length === 0) {
    return c.json({ error: "title is required" }, 400);
  }
  const { rows } = await pg.query(
    "INSERT INTO todos (title) VALUES ($1) RETURNING id, title, done",
    [title],
  );
  return c.json(rows[0], 201);
});

app.get("/api/todos", async (c) => {
  const { rows } = await pg.query("SELECT id, title, done FROM todos ORDER BY id");
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

app.get("/api/documents", async (c) => {
  const { rows } = await pg.query(
    "SELECT id, key, name, mime_type, size, owner_id FROM documents ORDER BY id",
  );
  return c.json(rows);
});

serve({ fetch: app.fetch, port: PORT }, () => {
  console.log(`hono example listening on http://localhost:${PORT}`);
});
