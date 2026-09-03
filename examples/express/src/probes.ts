import { createHash, randomBytes } from "node:crypto";
import { setTimeout as delay } from "node:timers/promises";
import linuxArm64 from "better-sqlite3/linux-arm64";
import linuxX64 from "better-sqlite3/linux-x64";
import express, { Router } from "express";
import { env } from "../ocel/vars";

const MAX_SLEEP_MS = 30_000;
const MAX_BODY = "8mb";
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

function hasSecret(): boolean {
  try {
    return env.SECRET_TOKEN.length > 0;
  } catch {
    return false;
  }
}

export const probes: Router = Router();

probes.get("/env", (_req, res) => {
  res.json({ greeting: env.GREETING, hasSecret: hasSecret(), arch: process.arch });
});

probes.get("/native", async (_req, res) => {
  const db = await openSqlite();
  try {
    const row = db.prepare("select sqlite_version() as version, 1 + 1 as answer").get() as {
      version: string;
      answer: number;
    };
    res.json({ arch: process.arch, sqlite: row.version, answer: row.answer });
  } finally {
    db.close();
  }
});

probes.get("/stream", async (_req, res) => {
  res.status(200);
  res.setHeader("content-type", "text/plain; charset=utf-8");
  res.setHeader("cache-control", "no-store, no-transform");
  res.flushHeaders();
  for (let i = 1; i < STREAM_CHUNKS; i++) {
    res.write(`ocel-stream-${i}\n`);
    await delay(STREAM_INTERVAL_MS);
  }
  res.end(`${STREAM_END}\n`);
});

probes.get("/status/:code", (req, res) => {
  const code = Number(req.params.code);
  if (code >= 300 && code < 400) {
    res.setHeader("location", "/api/probes/status/204");
  }
  res.status(code).json({ status: code });
});

probes.all(
  ["/echo", "/echo/{*rest}"],
  express.json({ limit: MAX_BODY, strict: false }),
  express.text({ limit: MAX_BODY, type: () => true }),
  (req, res) => {
    res.json({
      method: req.method,
      path: req.originalUrl.split("?")[0],
      query: req.query,
      header: req.get("x-ocel-probe") ?? null,
      body: req.body === undefined || req.body === "" ? null : req.body,
    });
  },
);

probes.post("/large", express.raw({ limit: MAX_BODY, type: () => true }), (req, res) => {
  const body = Buffer.isBuffer(req.body) ? req.body : Buffer.alloc(0);
  res.json({
    bytes: body.byteLength,
    sha256: createHash("sha256").update(body).digest("hex"),
  });
});

probes.get("/large", (req, res) => {
  const bytes = Number(req.query.bytes ?? 0);
  if (!Number.isInteger(bytes) || bytes < 0 || bytes > 8 * 1024 * 1024) {
    res.status(400).json({ error: "bytes must be an integer between 0 and 8388608" });
    return;
  }
  const body = randomBytes(bytes);
  res.setHeader("content-type", "application/octet-stream");
  res.setHeader("x-ocel-sha256", createHash("sha256").update(body).digest("hex"));
  res.setHeader("content-length", String(body.byteLength));
  res.end(body);
});

probes.get("/sleep", async (req, res) => {
  const ms = Number(req.query.ms ?? 0);
  if (!Number.isInteger(ms) || ms < 0 || ms > MAX_SLEEP_MS) {
    res.status(400).json({ error: `ms must be an integer between 0 and ${MAX_SLEEP_MS}` });
    return;
  }
  await delay(ms);
  res.json({ slept: ms });
});
