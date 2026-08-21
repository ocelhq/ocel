import express from "express";
import { migrate, pg } from "../ocel/index";

const app = express();
app.use(express.json());

const PORT = Number(process.env.PORT ?? 3106);

app.get("/api/health", (_req, res) => {
  res.json({ ok: true });
});

app.get("/api/status/:code", (req, res) => {
  res.status(Number(req.params.code)).json({ framework: "express" });
});

app.all("/api/echo/{*rest}", (req, res) => {
  res.json({
    framework: "express",
    method: req.method,
    path: req.path,
    query: req.query,
    probeHeader: req.get("x-ocel-probe") ?? null,
    body: req.body ?? null,
  });
});

app.post("/api/bootstrap", async (_req, res) => {
  await migrate();
  res.status(204).end();
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

app.put("/api/todos/:id", async (req, res) => {
  const { title, done } = req.body ?? {};
  if (
    typeof title !== "string" ||
    title.length === 0 ||
    typeof done !== "boolean"
  ) {
    res.status(400).json({ error: "title and done are required" });
    return;
  }
  const { rows } = await pg.query(
    "UPDATE todos SET title = $1, done = $2 WHERE id = $3 RETURNING id, title, done",
    [title, done, Number(req.params.id)],
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

app.listen(PORT, () => {
  console.log(`with-transforms example listening on http://localhost:${PORT}`);
});
