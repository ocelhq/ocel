import { createHash, randomBytes } from "node:crypto";
import { setTimeout as delay } from "node:timers/promises";
import { gunzipSync, gzipSync } from "node:zlib";
import linuxArm64 from "better-sqlite3/linux-arm64";
import linuxX64 from "better-sqlite3/linux-x64";

const MOUNT = "/api/probes";
const MAX_SLEEP_MS = 60_000;
const MAX_BODY = 8 * 1024 * 1024;
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

function bounded(raw: string | null, fallback: number, max: number): number | undefined {
  const value = raw === null ? fallback : Number(raw);
  return Number.isInteger(value) && value >= 0 && value <= max ? value : undefined;
}

function json(body: unknown, init?: ResponseInit): Response {
  return new Response(JSON.stringify(body), {
    ...init,
    headers: { "content-type": "application/json", ...init?.headers },
  });
}

async function echoBody(request: Request): Promise<unknown> {
  const text = await request.text();
  if (text.length === 0) {
    return null;
  }
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

async function nativeProbe(): Promise<Response> {
  const db = await openSqlite();
  try {
    const row = db.prepare("select sqlite_version() as version, 1 + 1 as answer").get() as {
      version: string;
      answer: number;
    };
    return json({ arch: process.arch, sqlite: row.version, answer: row.answer });
  } finally {
    db.close();
  }
}

function streamProbe(): Response {
  const encoder = new TextEncoder();
  const body = new ReadableStream<Uint8Array>({
    async start(controller) {
      for (let i = 1; i < STREAM_CHUNKS; i++) {
        controller.enqueue(encoder.encode(`ocel-stream-${i}\n`));
        await delay(STREAM_INTERVAL_MS);
      }
      controller.enqueue(encoder.encode(`${STREAM_END}\n`));
      controller.close();
    },
  });
  return new Response(body, {
    status: 200,
    headers: {
      "content-type": "text/plain; charset=utf-8",
      "cache-control": "no-store, no-transform",
    },
  });
}

function sseProbe(url: URL): Response {
  const events = bounded(url.searchParams.get("events"), 3, MAX_EVENTS);
  const gap = bounded(url.searchParams.get("gap"), 1000, MAX_GAP_MS);
  if (events === undefined || gap === undefined) {
    return json(
      { error: `events must be 0..${MAX_EVENTS} and gap 0..${MAX_GAP_MS}` },
      { status: 400 },
    );
  }
  const encoder = new TextEncoder();
  const body = new ReadableStream<Uint8Array>({
    async start(controller) {
      for (let i = 1; i <= events; i++) {
        controller.enqueue(encoder.encode(`id: ${i}\ndata: ocel-sse-${i} ${Date.now()}\n\n`));
        if (i < events) {
          await delay(gap);
        }
      }
      controller.enqueue(encoder.encode("event: end\ndata: ocel-sse-end\n\n"));
      controller.close();
    },
  });
  return new Response(body, {
    status: 200,
    headers: {
      "content-type": "text/event-stream",
      "cache-control": "no-store, no-transform",
    },
  });
}

function statusProbe(code: number): Response {
  const headers: Record<string, string> =
    code >= 300 && code < 400 ? { location: `${MOUNT}/status/204` } : {};
  if (code === 204 || code === 304) {
    return new Response(null, { status: code, headers });
  }
  return json({ status: code }, { status: code, headers });
}

async function echoProbe(request: Request, url: URL, tail: string[]): Promise<Response> {
  return json({
    method: request.method,
    path: url.pathname,
    search: url.search,
    segments: tail.map((segment) => decodeURIComponent(segment)),
    query: Object.fromEntries(url.searchParams),
    header: request.headers.get("x-ocel-probe"),
    body: await echoBody(request),
  });
}

function headersProbe(request: Request): Response {
  return json({
    headers: Object.fromEntries(request.headers),
    remote: null,
    ip: null,
    protocol: new URL(request.url).protocol.replace(/:$/, ""),
    hostname: new URL(request.url).hostname,
  });
}

function authProbe(): Response {
  return json(
    { error: "unauthorized" },
    {
      status: 401,
      headers: { "www-authenticate": 'Bearer realm="ocel"', "x-ocel-number": String(42) },
    },
  );
}

function methodProbe(request: Request): Response {
  if (request.method !== "GET" && request.method !== "HEAD") {
    return json(
      { error: `${request.method} is not allowed` },
      { status: 405, headers: { allow: "GET, HEAD" } },
    );
  }
  return json({ method: request.method });
}

function corsProbe(request: Request): Response {
  const headers = {
    "access-control-allow-origin": "*",
    "access-control-allow-methods": "GET, POST, OPTIONS",
    "access-control-allow-headers": "content-type, x-ocel-probe",
  };
  if (request.method === "OPTIONS") {
    return new Response(null, { status: 204, headers });
  }
  return json({ method: request.method }, { headers });
}

function cookiesProbe(url: URL): Response {
  const count = bounded(url.searchParams.get("count"), 1, MAX_COOKIES);
  if (count === undefined) {
    return json(
      { error: `count must be an integer between 0 and ${MAX_COOKIES}` },
      { status: 400 },
    );
  }
  const headers = new Headers({ "content-type": "application/json" });
  for (let i = 1; i <= count; i++) {
    headers.append("set-cookie", `ocel-cookie-${i}=value-${i}; Path=/; HttpOnly`);
  }
  return new Response(JSON.stringify({ count }), { headers });
}

function compressProbe(request: Request): Response {
  const body = Buffer.from(JSON.stringify(COMPRESSIBLE), "utf8");
  const headers: Record<string, string> = {
    "content-type": "application/json",
    vary: "accept-encoding",
    "x-ocel-sha256": sha256(body),
  };
  if (/\bgzip\b/.test(request.headers.get("accept-encoding") ?? "")) {
    const zipped = gzipSync(body);
    return new Response(zipped, {
      headers: {
        ...headers,
        "content-encoding": "gzip",
        "content-length": String(zipped.byteLength),
      },
    });
  }
  return new Response(body, {
    headers: { ...headers, "content-length": String(body.byteLength) },
  });
}

async function inflateProbe(request: Request): Promise<Response> {
  const encoding = request.headers.get("content-encoding");
  const raw = Buffer.from(await request.arrayBuffer());
  const body = encoding === "gzip" ? gunzipSync(raw) : raw;
  return json({ encoding, bytes: body.byteLength, sha256: sha256(body) });
}

async function multipartProbe(request: Request): Promise<Response> {
  if (!request.headers.get("content-type")?.startsWith("multipart/form-data")) {
    return json({ error: "multipart/form-data only" }, { status: 415 });
  }
  const form = await request.formData();
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
  return json({ fields, files });
}

async function largeIn(request: Request): Promise<Response> {
  const body = Buffer.from(await request.arrayBuffer());
  if (body.byteLength > MAX_BODY) {
    return json({ error: "bad request" }, { status: 413 });
  }
  return json({ bytes: body.byteLength, sha256: sha256(body) });
}

function largeOut(url: URL): Response {
  const bytes = bounded(url.searchParams.get("bytes"), 0, MAX_BODY);
  if (bytes === undefined) {
    return json({ error: `bytes must be an integer between 0 and ${MAX_BODY}` }, { status: 400 });
  }
  const body = randomBytes(bytes);
  const headers: Record<string, string> = {
    "content-type": "application/octet-stream",
    "x-ocel-sha256": sha256(body),
  };
  if (url.searchParams.get("chunked") === null) {
    return new Response(body, {
      status: 200,
      headers: { ...headers, "content-length": String(body.byteLength) },
    });
  }
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      for (let offset = 0; offset < body.byteLength; offset += CHUNK_BYTES) {
        controller.enqueue(body.subarray(offset, offset + CHUNK_BYTES));
      }
      controller.close();
    },
  });
  return new Response(stream, { status: 200, headers });
}

function runtimeProbe(): Response {
  const now = new Date();
  return json({
    nodeEnv: process.env.NODE_ENV ?? null,
    tz: process.env.TZ ?? null,
    zone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    offsetMinutes: now.getTimezoneOffset(),
    now: now.toISOString(),
    node: process.versions.node,
  });
}

async function sleepProbe(url: URL): Promise<Response> {
  const ms = bounded(url.searchParams.get("ms"), 0, MAX_SLEEP_MS);
  if (ms === undefined) {
    return json({ error: `ms must be an integer between 0 and ${MAX_SLEEP_MS}` }, { status: 400 });
  }
  await delay(ms);
  return json({ slept: ms });
}

async function handle(request: Request): Promise<Response> {
  const url = new URL(request.url);
  const rest = url.pathname.slice(MOUNT.length);
  const [, head, ...tail] = rest.split("/");

  if (head === "echo") {
    return echoProbe(request, url, tail);
  }
  if (head === "method") {
    return methodProbe(request);
  }
  if (head === "cors") {
    return corsProbe(request);
  }
  if (request.method === "GET") {
    if (head === "native") {
      return nativeProbe();
    }
    if (head === "stream") {
      return streamProbe();
    }
    if (head === "sse") {
      return sseProbe(url);
    }
    if (head === "status" && tail.length === 1) {
      return statusProbe(Number(tail[0]));
    }
    if (head === "headers") {
      return headersProbe(request);
    }
    if (head === "auth") {
      return authProbe();
    }
    if (head === "cookies") {
      return cookiesProbe(url);
    }
    if (head === "compress") {
      return compressProbe(request);
    }
    if (head === "large") {
      return largeOut(url);
    }
    if (head === "runtime") {
      return runtimeProbe();
    }
    if (head === "sleep") {
      return sleepProbe(url);
    }
  }
  if (request.method === "POST") {
    if (head === "large") {
      return largeIn(request);
    }
    if (head === "inflate") {
      return inflateProbe(request);
    }
    if (head === "multipart") {
      return multipartProbe(request);
    }
  }
  return json({ error: `no probe at ${url.pathname}` }, { status: 404 });
}

export const GET = handle;
export const POST = handle;
export const PUT = handle;
export const PATCH = handle;
export const DELETE = handle;
export const HEAD = handle;
export const OPTIONS = handle;
