import { createHash, randomBytes } from "node:crypto";
import { setTimeout as delay } from "node:timers/promises";
import linuxArm64 from "better-sqlite3/linux-arm64";
import linuxX64 from "better-sqlite3/linux-x64";
import type { FastifyInstance } from "fastify";

const MAX_SLEEP_MS = 30_000;
export const MAX_BODY = 8 * 1024 * 1024;
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

function greeting(): string {
  return process.env.GREETING ?? "hello";
}

function hasSecret(): boolean {
  return (process.env.SECRET_TOKEN ?? "").length > 0;
}

function echoBody(body: unknown): unknown {
  if (body === undefined || body === null) {
    return null;
  }
  if (!Buffer.isBuffer(body)) {
    return body;
  }
  const text = body.toString("utf8");
  if (text.length === 0) {
    return null;
  }
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

export async function probes(app: FastifyInstance): Promise<void> {
  app.addContentTypeParser("*", { parseAs: "buffer", bodyLimit: MAX_BODY }, (_req, body, done) => {
    done(null, body);
  });

  app.get("/env", async () => ({
    greeting: greeting(),
    hasSecret: hasSecret(),
    arch: process.arch,
  }));

  app.get("/native", async () => {
    const db = await openSqlite();
    try {
      const row = db.prepare("select sqlite_version() as version, 1 + 1 as answer").get() as {
        version: string;
        answer: number;
      };
      return { arch: process.arch, sqlite: row.version, answer: row.answer };
    } finally {
      db.close();
    }
  });

  app.get("/stream", async (_request, reply) => {
    reply.hijack();
    reply.raw.writeHead(200, {
      "content-type": "text/plain; charset=utf-8",
      "cache-control": "no-store, no-transform",
    });
    for (let i = 1; i < STREAM_CHUNKS; i++) {
      reply.raw.write(`ocel-stream-${i}\n`);
      await delay(STREAM_INTERVAL_MS);
    }
    reply.raw.end(`${STREAM_END}\n`);
  });

  app.get<{ Params: { code: string } }>("/status/:code", async (request, reply) => {
    const code = Number(request.params.code);
    if (code >= 300 && code < 400) {
      reply.header("location", "/api/probes/status/204");
    }
    if (code === 204 || code === 304) {
      return reply.code(code).send();
    }
    return reply.code(code).send({ status: code });
  });

  for (const url of ["/echo", "/echo/*"]) {
    app.route({
      method: ["GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"],
      url,
      bodyLimit: MAX_BODY,
      handler: async (request) => ({
        method: request.method,
        path: request.url.split("?")[0],
        query: request.query,
        header: request.headers["x-ocel-probe"] ?? null,
        body: echoBody(request.body),
      }),
    });
  }

  app.post("/large", { bodyLimit: MAX_BODY }, async (request) => {
    const body = Buffer.isBuffer(request.body) ? request.body : Buffer.alloc(0);
    return {
      bytes: body.byteLength,
      sha256: createHash("sha256").update(body).digest("hex"),
    };
  });

  app.get<{ Querystring: { bytes?: string } }>("/large", async (request, reply) => {
    const bytes = Number(request.query.bytes ?? 0);
    if (!Number.isInteger(bytes) || bytes < 0 || bytes > MAX_BODY) {
      return reply
        .code(400)
        .send({ error: `bytes must be an integer between 0 and ${MAX_BODY}` });
    }
    const body = randomBytes(bytes);
    return reply
      .header("content-type", "application/octet-stream")
      .header("x-ocel-sha256", createHash("sha256").update(body).digest("hex"))
      .send(body);
  });

  app.get<{ Querystring: { ms?: string } }>("/sleep", async (request, reply) => {
    const ms = Number(request.query.ms ?? 0);
    if (!Number.isInteger(ms) || ms < 0 || ms > MAX_SLEEP_MS) {
      return reply
        .code(400)
        .send({ error: `ms must be an integer between 0 and ${MAX_SLEEP_MS}` });
    }
    await delay(ms);
    return { slept: ms };
  });
}
