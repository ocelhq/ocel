import express from "express";
import { pg } from "../ocel/index";

// postgres("main") is resolved from the environment `ocel dev` injects, so the
// server never sees a connection string of its own.
const app = express();
app.use(express.json());

const PORT = Number(process.env.PORT ?? 3102);

// Readiness probe the e2e harness polls before hitting the CRUD routes.
app.get("/health", (_req, res) => {
  res.json({ ok: true });
});

app.post("/todos", async (req, res) => {
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

app.get("/todos", async (_req, res) => {
  const { rows } = await pg.query(
    "SELECT id, title, done FROM todos ORDER BY id",
  );
  res.json(rows);
});

app.get("/todos/:id", async (req, res) => {
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

app.delete("/todos/:id", async (req, res) => {
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
  console.log(`express example listening on http://localhost:${PORT}`);
});
