import express from "express";
import { pg } from "../ocel/index";

const app = express();
app.use(express.json());

const PORT = Number(process.env.PORT ?? 3106);

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

app.listen(PORT, () => {
  console.log(`with-transforms example listening on http://localhost:${PORT}`);
});
