import express from "express";

export const FRAMEWORK = "express";

export const MARKER = `ocel-bench:${FRAMEWORK}:v1`;

export const app = express();

app.use(express.json());

app.get("/", (_req, res) => {
  res.type("text/plain").send(MARKER);
});

app.get("/health", (_req, res) => {
  res.json({ ok: true, framework: FRAMEWORK });
});

app.get("/status/:code", (req, res) => {
  res.status(Number(req.params.code)).json({ framework: FRAMEWORK });
});

app.all("/echo/{*rest}", (req, res) => {
  res.json({
    framework: FRAMEWORK,
    method: req.method,
    path: req.path,
    query: req.query,
    probeHeader: req.get("x-ocel-probe") ?? null,
    body: req.body ?? null,
  });
});
