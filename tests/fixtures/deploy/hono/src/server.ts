import { serve } from "@hono/node-server";
import { Hono } from "hono";
import { probes } from "./probes";

const APP_NAME = process.env.APP_NAME ?? "web";
const PORT = Number(process.env.PORT ?? 3103);

const OCEL_SVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64" role="img" aria-label="ocel"><rect width="64" height="64" rx="14" fill="#0b0f14"/><circle cx="24" cy="27" r="5" fill="#f2b705"/><circle cx="42" cy="27" r="5" fill="#f2b705"/><path d="M20 42c4 5 20 5 24 0" stroke="#f2b705" stroke-width="4" fill="none" stroke-linecap="round"/></svg>\n`;
const OCEL_SVG_BYTES = Buffer.from(OCEL_SVG, "utf8");

const app = new Hono();

app.get("/health", (c) => c.json({ ok: true, app: APP_NAME }));

app.get("/ocel.svg", (c) => {
  c.header("content-type", "image/svg+xml");
  c.header("content-length", String(OCEL_SVG_BYTES.byteLength));
  return c.body(OCEL_SVG_BYTES);
});

app.route("/api/probes", probes);

serve({ fetch: app.fetch, port: PORT }, () => {
  console.log(`hono listening on http://localhost:${PORT}`);
});
