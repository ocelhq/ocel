import express from "express";

const PORT = Number(process.env.PORT ?? 3110);

const app = express();

app.get("/health", (_req, res) => {
  res.json({ ok: true, app: "web" });
});

app.listen(PORT, () => {
  console.log(`pre-build failing fixture listening on http://localhost:${PORT}`);
});
