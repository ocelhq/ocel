import express from "express";
import { createRouteHandler } from "ocel/blob/express";
import { pg, uploads } from "../ocel/index";

const app = express();
app.use(express.json());

const PORT = Number(process.env.PORT ?? 3102);

app.get("/api/health", (_req, res) => {
  res.json({ ok: true });
});

app.post("/api/todos", async (req, res) => {
  const { title } = req.body ?? {};
  if (typeof title !== "string" || title.length === 0) {
    res.status(400).json({ error: "title is required" });
    return;
  }
  const { rows } = await pg.query(
    "INSERT INTO todos (title) VALUES ($1) RETURNING id, title, done",
    [title],
  );
  res.status(201).json(rows[0]);
});

app.get("/api/todos", async (_req, res) => {
  const { rows } = await pg.query(
    "SELECT id, title, done FROM todos ORDER BY id",
  );
  res.json(rows);
});

app.get("/api/todos/:id", async (req, res) => {
  const { rows } = await pg.query(
    "SELECT id, title, done FROM todos WHERE id = $1",
    [Number(req.params.id)],
  );
  if (rows.length === 0) {
    res.status(404).json({ error: "not found" });
    return;
  }
  res.json(rows[0]);
});

app.delete("/api/todos/:id", async (req, res) => {
  const { rowCount } = await pg.query("DELETE FROM todos WHERE id = $1", [
    Number(req.params.id),
  ]);
  if (!rowCount) {
    res.status(404).json({ error: "not found" });
    return;
  }
  res.status(204).end();
});

app.all("/api/upload", createRouteHandler(uploads));

app.get("/api/documents", async (_req, res) => {
  const { rows } = await pg.query(
    "SELECT id, key, name, mime_type, size, owner_id, thumbnail_key FROM documents ORDER BY id",
  );
  res.json(rows);
});

app.listen(PORT, () => {
  console.log(`express example listening on http://localhost:${PORT}`);
});
