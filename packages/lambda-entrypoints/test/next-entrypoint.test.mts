import net from "node:net";
import { EventEmitter } from "node:events";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, expect, test } from "vitest";
import { writeNextProjectFixture } from "./next-project-fixture.mjs";

const launcherModule = `module.exports = {
  async handler(req, res, ctx) {
    if (req.url === "/__request-meta") {
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify(ctx.requestMeta));
      return;
    }
    const cache = globalThis.__incrementalCache;
    res.setHeader("content-type", "application/json");
    if (!cache) {
      res.statusCode = 500;
      res.end(JSON.stringify({ error: "incrementalCache missing" }));
      return;
    }
    const key = await cache.generateSimpleCacheKey("launcher-invocation");
    const hit = await cache.get(key, { kind: "FETCH", revalidate: 60 });
    if (hit && hit.value && !hit.isStale) {
      res.end(JSON.stringify({ from: "cache", random: JSON.parse(hit.value.data.body) }));
      return;
    }
    const random = Math.random();
    await cache.set(
      key,
      {
        kind: "FETCH",
        data: { headers: {}, body: JSON.stringify(random), status: 200, url: "" },
        revalidate: 60,
      },
      { fetchCache: true },
    );
    res.end(JSON.stringify({ from: "render", random }));
  },
};
`;

type Msg = { type: string; payload: any };

const messages: Msg[] = [];
const bus = new EventEmitter();
const controlConns = new Set<net.Socket>();
let controlServer: net.Server;
let dir: string;
let port: number;

function waitFor(pred: () => boolean, timeoutMs = 5000): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    if (pred()) return resolve();
    const onMsg = (): void => {
      if (pred()) {
        clearTimeout(timer);
        bus.off("msg", onMsg);
        resolve();
      }
    };
    const timer = setTimeout(() => {
      bus.off("msg", onMsg);
      reject(new Error(`timeout; messages so far: ${JSON.stringify(messages)}`));
    }, timeoutMs);
    bus.on("msg", onMsg);
  });
}

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-next-entry-"));

  const sockPath = join(dir, "control.sock");
  controlServer = net.createServer((conn) => {
    controlConns.add(conn);
    conn.on("close", () => controlConns.delete(conn));
    let buf = "";
    conn.on("data", (d) => {
      buf += d.toString();
      let idx: number;
      while ((idx = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, idx);
        buf = buf.slice(idx + 1);
        if (line.trim()) {
          messages.push(JSON.parse(line));
          bus.emit("msg");
        }
      }
    });
  });
  await new Promise<void>((resolve) => controlServer.listen(sockPath, resolve));

  const projectDir = join(dir, "project");
  await writeNextProjectFixture(projectDir);
  const launcherPath = join(projectDir, "__next_launcher.cjs");
  await writeFile(launcherPath, launcherModule);

  process.env.OCEL_CONTROL_SOCKET = sockPath;
  process.env.OCEL_HANDLER = launcherPath;
  await import("../src/next/entrypoint.mjs");
  await waitFor(() => messages.some((m) => m.type === "server-ready"));
  port = messages.find((m) => m.type === "server-ready")!.payload.httpPort;
});

afterAll(async () => {
  for (const c of controlConns) c.end();
  await new Promise<void>((resolve) => controlServer.close(() => resolve()));
  await rm(dir, { recursive: true, force: true });
});

test("unstable_cache's flow works through the published incremental cache", async () => {
  const first = await fetch(`http://127.0.0.1:${port}/api/anything`);
  expect(first.status).toBe(200);
  const rendered = await first.json();
  expect(rendered.from).toBe("render");

  const second = await fetch(`http://127.0.0.1:${port}/api/anything`);
  expect(second.status).toBe(200);
  const cached = await second.json();
  expect(cached.from).toBe("cache");
  expect(cached.random).toBe(rendered.random);
});

test("the server-action self-fetch origin is the loopback the app bound", async () => {
  expect(process.env.__NEXT_PRIVATE_ORIGIN).toBe(`http://127.0.0.1:${port}`);

  const reached = await fetch(`${process.env.__NEXT_PRIVATE_ORIGIN}/api/anything`);
  expect(reached.status).toBe(200);
});

function requestMeta(init?: RequestInit): Promise<any> {
  return fetch(`http://127.0.0.1:${port}/__request-meta`, init).then((r) => r.json());
}

const resume = { method: "POST", headers: { "next-resume": "1" }, body: "[1,{}]" };

test("a PPR resume runs under minimal mode", async () => {
  expect(await requestMeta(resume)).toMatchObject({ minimalMode: true });
});

test.each([
  ["a document GET", undefined],
  ["a POST that is not a resume", { method: "POST", body: "x" }],
  ["a GET carrying the resume header", { headers: { "next-resume": "1" } }],
])("%s does not", async (_name, init) => {
  const meta = await requestMeta(init);

  expect(meta.minimalMode).toBeUndefined();
  expect(meta.relativeProjectDir).toBeTruthy();
});
