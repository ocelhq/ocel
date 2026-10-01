import express from "express";
import { cache } from "../infra/index";

const APP_NAME = "web";
const PORT = Number(process.env.PORT ?? 3107);

const OCEL_SVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64" role="img" aria-label="ocel"><rect width="64" height="64" rx="14" fill="#0b0f14"/><circle cx="24" cy="27" r="5" fill="#f2b705"/><circle cx="42" cy="27" r="5" fill="#f2b705"/><path d="M20 42c4 5 20 5 24 0" stroke="#f2b705" stroke-width="4" fill="none" stroke-linecap="round"/></svg>\n`;
const OCEL_SVG_BYTES = Buffer.from(OCEL_SVG, "utf8");

const app = express();
app.use(express.json());

app.get("/health", (_req, res) => {
  res.json({ ok: true, app: APP_NAME });
});

app.get("/ocel.svg", (_req, res) => {
  res.setHeader("content-type", "image/svg+xml");
  res.setHeader("content-length", String(OCEL_SVG_BYTES.byteLength));
  res.end(OCEL_SVG_BYTES);
});

app.get("/api/kv", (_req, res) => {
  const { hostname, port, username, password } = new URL(cache.connectionString);
  res.json({ host: hostname, port: Number(port), username, hasPassword: password.length > 0 });
});

app.get("/api/kv/ping", async (_req, res) => {
  res.json({ pong: await cache.client.ping() });
});

app.put("/api/kv/entries/:key", async (req, res) => {
  const value = (req.body as { value?: unknown } | undefined)?.value;
  if (typeof value !== "string") {
    res.status(400).json({ error: "the body names no string value" });
    return;
  }
  await cache.entry.set({ key: req.params.key }, value);
  res.status(204).end();
});

app.get("/api/kv/entries/:key", async (req, res) => {
  const value = await cache.entry.get({ key: req.params.key });
  if (value === undefined) {
    res.status(404).json({ error: "no such entry" });
    return;
  }
  res.json({ value });
});

app.listen(PORT, () => {
  console.log(`kv fixture listening on http://localhost:${PORT}`);
});
