import { Hono } from "hono";

export const FRAMEWORK = "hono";

export const MARKER = `ocel-bench:${FRAMEWORK}:v1`;

export const app = new Hono();

app.get("/", (c) => c.text(MARKER));

app.get("/health", (c) => c.json({ ok: true, framework: FRAMEWORK }));

app.get("/status/:code", (c) => c.json({ framework: FRAMEWORK }, Number(c.req.param("code")) as 200));

app.all("/echo/*", async (c) => {
  const url = new URL(c.req.url);
  return c.json({
    framework: FRAMEWORK,
    method: c.req.method,
    path: url.pathname,
    query: Object.fromEntries(url.searchParams),
    probeHeader: c.req.header("x-ocel-probe") ?? null,
    body: c.req.header("content-type")?.includes("application/json") ? await c.req.json() : null,
  });
});
