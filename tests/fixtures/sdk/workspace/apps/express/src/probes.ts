import { createHash, randomBytes } from "node:crypto";
import { Readable } from "node:stream";
import { setTimeout as delay } from "node:timers/promises";
import { gzipSync } from "node:zlib";
import linuxArm64 from "better-sqlite3/linux-arm64";
import linuxX64 from "better-sqlite3/linux-x64";
import express, { type Request, Router } from "express";
import { env } from "../../../ocel/vars";

const MAX_SLEEP_MS = 60_000;
const MAX_BODY = "8mb";
const MAX_BYTES = 8 * 1024 * 1024;
const STREAM_CHUNKS = 5;
const STREAM_INTERVAL_MS = 200;
const STREAM_END = "ocel-stream-end";
const CHUNK_BYTES = 64 * 1024;
const MAX_COOKIES = 10;
const MAX_EVENTS = 10;
const MAX_GAP_MS = 60_000;
const COMPRESSIBLE = { marker: "ocel-compress", filler: "ocel ".repeat(1024) };

async function openSqlite() {
  const Database =
    process.platform === "linux"
      ? process.arch === "arm64"
        ? linuxArm64
        : linuxX64
      : (await import("better-sqlite3")).default;
  return new Database(":memory:");
}

function sha256(bytes: Buffer): string {
  return createHash("sha256").update(bytes).digest("hex");
}

function bounded(raw: unknown, fallback: number, max: number): number | undefined {
  const value = raw === undefined ? fallback : Number(raw);
  return Number.isInteger(value) && value >= 0 && value <= max ? value : undefined;
}

function segmentsOf(req: Request): string[] {
  const rest = req.params.rest;
  return rest === undefined ? [] : Array.isArray(rest) ? rest : [rest];
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

probes.get("/sse", async (req, res) => {
  const events = bounded(req.query.events, 3, MAX_EVENTS);
  const gap = bounded(req.query.gap, 1000, MAX_GAP_MS);
  if (events === undefined || gap === undefined) {
    res.status(400).json({ error: `events must be 0..${MAX_EVENTS} and gap 0..${MAX_GAP_MS}` });
    return;
  }
  res.status(200);
  res.setHeader("content-type", "text/event-stream");
  res.setHeader("cache-control", "no-store, no-transform");
  res.flushHeaders();
  for (let i = 1; i <= events; i++) {
    res.write(`id: ${i}\ndata: ocel-sse-${i} ${Date.now()}\n\n`);
    if (i < events) {
      await delay(gap);
    }
  }
  res.end("event: end\ndata: ocel-sse-end\n\n");
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
    const [path, ...search] = req.originalUrl.split("?");
    res.json({
      method: req.method,
      path,
      search: search.length === 0 ? "" : `?${search.join("?")}`,
      segments: segmentsOf(req),
      query: req.query,
      header: req.get("x-ocel-probe") ?? null,
      body: req.body === undefined || req.body === "" ? null : req.body,
    });
  },
);

probes.get("/headers", (req, res) => {
  res.json({
    headers: req.headers,
    remote: req.socket.remoteAddress ?? null,
    ip: req.ip ?? null,
    protocol: req.protocol,
    hostname: req.hostname,
  });
});

probes.get("/auth", (_req, res) => {
  res.setHeader("www-authenticate", 'Bearer realm="ocel"');
  res.setHeader("x-ocel-number", 42);
  res.status(401).json({ error: "unauthorized" });
});

probes.all("/method", (req, res) => {
  if (req.method !== "GET" && req.method !== "HEAD") {
    res.setHeader("allow", "GET, HEAD");
    res.status(405).json({ error: `${req.method} is not allowed` });
    return;
  }
  res.json({ method: req.method });
});

probes.all("/cors", (req, res) => {
  res.setHeader("access-control-allow-origin", "*");
  res.setHeader("access-control-allow-methods", "GET, POST, OPTIONS");
  res.setHeader("access-control-allow-headers", "content-type, x-ocel-probe");
  if (req.method === "OPTIONS") {
    res.status(204).end();
    return;
  }
  res.json({ method: req.method });
});

probes.get("/cookies", (req, res) => {
  const count = bounded(req.query.count, 1, MAX_COOKIES);
  if (count === undefined) {
    res.status(400).json({ error: `count must be an integer between 0 and ${MAX_COOKIES}` });
    return;
  }
  for (let i = 1; i <= count; i++) {
    res.append("set-cookie", `ocel-cookie-${i}=value-${i}; Path=/; HttpOnly`);
  }
  res.json({ count });
});

probes.get("/compress", (req, res) => {
  const body = Buffer.from(JSON.stringify(COMPRESSIBLE), "utf8");
  res.setHeader("content-type", "application/json");
  res.setHeader("vary", "accept-encoding");
  res.setHeader("x-ocel-sha256", sha256(body));
  if (/\bgzip\b/.test(req.get("accept-encoding") ?? "")) {
    const zipped = gzipSync(body);
    res.setHeader("content-encoding", "gzip");
    res.setHeader("content-length", String(zipped.byteLength));
    res.end(zipped);
    return;
  }
  res.setHeader("content-length", String(body.byteLength));
  res.end(body);
});

probes.post("/inflate", express.raw({ limit: MAX_BODY, type: () => true }), (req, res) => {
  const body = Buffer.isBuffer(req.body) ? req.body : Buffer.alloc(0);
  res.json({
    encoding: req.get("content-encoding") ?? null,
    bytes: body.byteLength,
    sha256: sha256(body),
  });
});

probes.post("/multipart", async (req, res) => {
  const type = req.get("content-type");
  if (!type?.startsWith("multipart/form-data")) {
    res.status(415).json({ error: "multipart/form-data only" });
    return;
  }
  const form = await new Response(Readable.toWeb(req) as ReadableStream, {
    headers: { "content-type": type },
  }).formData();
  const fields: Record<string, string> = {};
  const files: { field: string; name: string; type: string; bytes: number; sha256: string }[] = [];
  for (const [field, value] of form) {
    if (typeof value === "string") {
      fields[field] = value;
    } else {
      const bytes = Buffer.from(await value.arrayBuffer());
      files.push({
        field,
        name: value.name,
        type: value.type,
        bytes: bytes.byteLength,
        sha256: sha256(bytes),
      });
    }
  }
  res.json({ fields, files });
});

probes.post("/large", express.raw({ limit: MAX_BODY, type: () => true }), (req, res) => {
  const body = Buffer.isBuffer(req.body) ? req.body : Buffer.alloc(0);
  res.json({ bytes: body.byteLength, sha256: sha256(body) });
});

probes.get("/large", async (req, res) => {
  const bytes = bounded(req.query.bytes, 0, MAX_BYTES);
  if (bytes === undefined) {
    res.status(400).json({ error: `bytes must be an integer between 0 and ${MAX_BYTES}` });
    return;
  }
  const body = randomBytes(bytes);
  res.setHeader("content-type", "application/octet-stream");
  res.setHeader("x-ocel-sha256", sha256(body));
  if (req.query.chunked === undefined) {
    res.setHeader("content-length", String(body.byteLength));
    res.end(body);
    return;
  }
  res.flushHeaders();
  for (let offset = 0; offset < body.byteLength; offset += CHUNK_BYTES) {
    if (!res.write(body.subarray(offset, offset + CHUNK_BYTES))) {
      await new Promise((resolve) => res.once("drain", resolve));
    }
  }
  res.end();
});

probes.get("/runtime", (_req, res) => {
  const now = new Date();
  res.json({
    nodeEnv: process.env.NODE_ENV ?? null,
    tz: process.env.TZ ?? null,
    zone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    offsetMinutes: now.getTimezoneOffset(),
    now: now.toISOString(),
    node: process.versions.node,
  });
});

probes.get("/sleep", async (req, res) => {
  const ms = bounded(req.query.ms, 0, MAX_SLEEP_MS);
  if (ms === undefined) {
    res.status(400).json({ error: `ms must be an integer between 0 and ${MAX_SLEEP_MS}` });
    return;
  }
  await delay(ms);
  res.json({ slept: ms });
});
