import { createHash, randomBytes } from "node:crypto";
import { setTimeout as delay } from "node:timers/promises";
import linuxArm64 from "better-sqlite3/linux-arm64";
import linuxX64 from "better-sqlite3/linux-x64";
import { Hono } from "hono";
import { bodyLimit } from "hono/body-limit";
import { stream } from "hono/streaming";
import type { ContentfulStatusCode, StatusCode } from "hono/utils/http-status";

const MAX_SLEEP_MS = 30_000;
const MAX_BODY = 8 * 1024 * 1024;
const STREAM_CHUNKS = 5;
const STREAM_INTERVAL_MS = 200;
const STREAM_END = "ocel-stream-end";

async function openSqlite() {
  const Database =
    process.platform === "linux"
      ? process.arch === "arm64"
        ? linuxArm64
        : linuxX64
      : (await import("better-sqlite3")).default;
  return new Database(":memory:");
}

function parseBody(text: string): unknown {
  if (text.length === 0) {
    return null;
  }
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

export const probes: Hono = new Hono();

probes.get("/native", async (c) => {
  const db = await openSqlite();
  try {
    const row = db.prepare("select sqlite_version() as version, 1 + 1 as answer").get() as {
      version: string;
      answer: number;
    };
    return c.json({ arch: process.arch, sqlite: row.version, answer: row.answer });
  } finally {
    db.close();
  }
});

probes.get("/stream", (c) => {
  c.header("content-type", "text/plain; charset=utf-8");
  c.header("cache-control", "no-store, no-transform");
  return stream(c, async (writer) => {
    for (let i = 1; i < STREAM_CHUNKS; i++) {
      await writer.write(`ocel-stream-${i}\n`);
      await delay(STREAM_INTERVAL_MS);
    }
    await writer.write(`${STREAM_END}\n`);
  });
});

probes.get("/status/:code", (c) => {
  const code = Number(c.req.param("code"));
  if (code >= 300 && code < 400) {
    c.header("location", "/api/probes/status/204");
  }
  if (code === 204 || code === 304) {
    return c.body(null, code as StatusCode);
  }
  return c.json({ status: code }, code as ContentfulStatusCode);
});

for (const path of ["/echo", "/echo/*"]) {
  probes.all(path, bodyLimit({ maxSize: MAX_BODY }), async (c) =>
    c.json({
      method: c.req.method,
      path: new URL(c.req.url).pathname,
      query: c.req.query(),
      header: c.req.header("x-ocel-probe") ?? null,
      body: parseBody(await c.req.text()),
    }),
  );
}

probes.post("/large", bodyLimit({ maxSize: MAX_BODY }), async (c) => {
  const body = Buffer.from(await c.req.arrayBuffer());
  return c.json({
    bytes: body.byteLength,
    sha256: createHash("sha256").update(body).digest("hex"),
  });
});

probes.get("/large", (c) => {
  const bytes = Number(c.req.query("bytes") ?? 0);
  if (!Number.isInteger(bytes) || bytes < 0 || bytes > MAX_BODY) {
    return c.json({ error: `bytes must be an integer between 0 and ${MAX_BODY}` }, 400);
  }
  const body = randomBytes(bytes);
  c.header("content-type", "application/octet-stream");
  c.header("x-ocel-sha256", createHash("sha256").update(body).digest("hex"));
  c.header("content-length", String(body.byteLength));
  return c.body(body);
});

probes.get("/sleep", async (c) => {
  const ms = Number(c.req.query("ms") ?? 0);
  if (!Number.isInteger(ms) || ms < 0 || ms > MAX_SLEEP_MS) {
    return c.json({ error: `ms must be an integer between 0 and ${MAX_SLEEP_MS}` }, 400);
  }
  await delay(ms);
  return c.json({ slept: ms });
});
