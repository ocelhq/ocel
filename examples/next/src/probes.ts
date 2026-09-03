import { createHash, randomBytes } from "node:crypto";
import { setTimeout as delay } from "node:timers/promises";
import linuxArm64 from "better-sqlite3/linux-arm64";
import linuxX64 from "better-sqlite3/linux-x64";
import { env } from "../ocel/vars";

const MOUNT = "/api/probes";
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

function hasSecret(): boolean {
  try {
    return env.SECRET_TOKEN.length > 0;
  } catch {
    return false;
  }
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

async function envProbe(): Promise<Response> {
  return json({ greeting: env.GREETING, hasSecret: hasSecret(), arch: process.arch });
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

function statusProbe(code: number): Response {
  const headers: Record<string, string> =
    code >= 300 && code < 400 ? { location: `${MOUNT}/status/204` } : {};
  if (code === 204 || code === 304) {
    return new Response(null, { status: code, headers });
  }
  return json({ status: code }, { status: code, headers });
}

async function echoProbe(request: Request, url: URL): Promise<Response> {
  return json({
    method: request.method,
    path: url.pathname,
    query: Object.fromEntries(url.searchParams),
    header: request.headers.get("x-ocel-probe"),
    body: await echoBody(request),
  });
}

async function largeIn(request: Request): Promise<Response> {
  const body = Buffer.from(await request.arrayBuffer());
  return json({
    bytes: body.byteLength,
    sha256: createHash("sha256").update(body).digest("hex"),
  });
}

function largeOut(url: URL): Response {
  const bytes = Number(url.searchParams.get("bytes") ?? 0);
  if (!Number.isInteger(bytes) || bytes < 0 || bytes > MAX_BODY) {
    return json({ error: `bytes must be an integer between 0 and ${MAX_BODY}` }, { status: 400 });
  }
  const body = randomBytes(bytes);
  return new Response(body, {
    status: 200,
    headers: {
      "content-type": "application/octet-stream",
      "content-length": String(body.byteLength),
      "x-ocel-sha256": createHash("sha256").update(body).digest("hex"),
    },
  });
}

async function sleepProbe(url: URL): Promise<Response> {
  const ms = Number(url.searchParams.get("ms") ?? 0);
  if (!Number.isInteger(ms) || ms < 0 || ms > MAX_SLEEP_MS) {
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
    return echoProbe(request, url);
  }
  if (request.method === "GET") {
    if (head === "env") {
      return envProbe();
    }
    if (head === "native") {
      return nativeProbe();
    }
    if (head === "stream") {
      return streamProbe();
    }
    if (head === "status" && tail.length === 1) {
      return statusProbe(Number(tail[0]));
    }
    if (head === "large") {
      return largeOut(url);
    }
    if (head === "sleep") {
      return sleepProbe(url);
    }
  }
  if (request.method === "POST" && head === "large") {
    return largeIn(request);
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
