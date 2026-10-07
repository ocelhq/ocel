import express from "express";
import { db } from "../infra/db";

const PORT = Number(process.env.PORT ?? 3109);

const app = express();

app.get("/health", (_req, res) => {
  res.json({ ok: true, app: "web" });
});

app.get("/notes", async (_req, res) => {
  const { rows } = await db.query<{ title: string }>("SELECT title FROM notes ORDER BY id");
  res.json({ titles: rows.map((row) => row.title) });
});

app.listen(PORT, () => {
  console.log(`pre-build fixture listening on http://localhost:${PORT}`);
});
