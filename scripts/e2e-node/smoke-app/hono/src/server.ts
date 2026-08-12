import { serve } from "@hono/node-server";
import { Hono } from "hono";

const MARKER = "ocel-node-smoke:hono:v1";

const app = new Hono();

const PORT = Number(process.env.PORT ?? 3202);

app.get("/", (c) => c.text(MARKER));

app.get("/health", (c) => c.json({ ok: true, framework: "hono" }));

app.get("/status/:code", (c) => c.json({ framework: "hono" }, Number(c.req.param("code")) as 200));

app.all("/echo/*", async (c) => {
  const url = new URL(c.req.url);
  return c.json({
    framework: "hono",
    method: c.req.method,
    path: url.pathname,
    query: Object.fromEntries(url.searchParams),
    probeHeader: c.req.header("x-ocel-probe") ?? null,
    body: c.req.header("content-type")?.includes("application/json") ? await c.req.json() : null,
  });
});

serve({ fetch: app.fetch, port: PORT }, () => {
  console.log(`${MARKER} listening on http://localhost:${PORT}`);
});
