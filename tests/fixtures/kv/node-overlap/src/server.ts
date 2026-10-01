import express from "express";
import { notes } from "../infra/index";

const PORT = Number(process.env.PORT ?? 3108);

const app = express();

app.get("/health", (_req, res) => {
  res.json({ ok: true, app: "web" });
});

app.get("/api/notes/:id", async (req, res) => {
  res.json({ value: (await notes.byId.get({ id: req.params.id })) ?? null });
});

app.listen(PORT, () => {
  console.log(`kv overlap fixture listening on http://localhost:${PORT}`);
});
