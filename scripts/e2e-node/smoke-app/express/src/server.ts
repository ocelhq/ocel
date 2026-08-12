import express from "express";

const MARKER = "ocel-node-smoke:express:v1";

const app = express();
app.use(express.json());

const PORT = Number(process.env.PORT ?? 3201);

app.get("/", (_req, res) => {
  res.type("text/plain").send(MARKER);
});

app.get("/health", (_req, res) => {
  res.json({ ok: true, framework: "express" });
});

app.get("/status/:code", (req, res) => {
  res.status(Number(req.params.code)).json({ framework: "express" });
});

app.all("/echo/{*rest}", (req, res) => {
  res.json({
    framework: "express",
    method: req.method,
    path: req.path,
    query: req.query,
    probeHeader: req.get("x-ocel-probe") ?? null,
    body: req.body ?? null,
  });
});

app.listen(PORT, () => {
  console.log(`${MARKER} listening on http://localhost:${PORT}`);
});
