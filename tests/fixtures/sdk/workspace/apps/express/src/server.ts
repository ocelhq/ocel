import express, { type NextFunction, type Request, type Response } from "express";
import { createRouteHandler } from "ocel/blob/express";
import { pg, uploads } from "../../../ocel/index";
import { probes } from "./probes";

const APP_NAME = "express";
const PORT = Number(process.env.PORT ?? 3102);

const OCEL_SVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64" role="img" aria-label="ocel"><rect width="64" height="64" rx="14" fill="#0b0f14"/><circle cx="24" cy="27" r="5" fill="#f2b705"/><circle cx="42" cy="27" r="5" fill="#f2b705"/><path d="M20 42c4 5 20 5 24 0" stroke="#f2b705" stroke-width="4" fill="none" stroke-linecap="round"/></svg>\n`;
const OCEL_SVG_BYTES = Buffer.from(OCEL_SVG, "utf8");

const app = express();

app.get("/health", (_req, res) => {
  res.json({ ok: true, app: APP_NAME });
});

app.get("/ocel.svg", (_req, res) => {
  res.setHeader("content-type", "image/svg+xml");
  res.setHeader("content-length", String(OCEL_SVG_BYTES.byteLength));
  res.end(OCEL_SVG_BYTES);
});

app.use("/api/probes", probes);

app.all("/api/upload", createRouteHandler(uploads));

app.use(express.json());

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
  const { rows } = await pg.query("SELECT id, title, done FROM todos ORDER BY id");
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

app.get("/api/documents", async (_req, res) => {
  const { rows } = await pg.query(
    "SELECT id, key, name, mime_type, size, owner_id FROM documents ORDER BY id",
  );
  res.json(rows);
});

function statusOf(error: unknown): number {
  const status = (error as { status?: unknown })?.status;
  return typeof status === "number" && Number.isInteger(status) && status >= 400 && status <= 599
    ? status
    : 500;
}

app.use((error: unknown, _req: Request, res: Response, _next: NextFunction) => {
  console.error(error);
  if (res.headersSent) {
    res.end();
    return;
  }
  const status = statusOf(error);
  res.status(status).json({ error: status === 500 ? "internal error" : "bad request" });
});

app.listen(PORT, () => {
  console.log(`workspace express app listening on http://localhost:${PORT}`);
});
