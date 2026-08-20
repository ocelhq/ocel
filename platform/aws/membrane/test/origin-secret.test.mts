import net from "node:net";
import http from "node:http";
import { EventEmitter } from "node:events";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, afterEach, beforeAll, describe, expect, test } from "vitest";

const SECRET = "9f8e7d6c5b4a39281706f5e4d3c2b1a0";

type Msg = { type: string; payload: any };

const messages: Msg[] = [];
const bus = new EventEmitter();
const controlConns = new Set<net.Socket>();
let controlServer: net.Server;
let sockDir: string;

type Membrane = typeof import("../src/shared/membrane.mts");
type Invoke = import("../src/shared/membrane.mts").Invoke;

let membrane: Membrane;

function waitFor(pred: () => boolean, timeoutMs = 3000): Promise<void> {
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

const doors: Record<string, (invoke: Invoke) => Promise<void>> = {
  serveEntry: (invoke) => membrane.serveEntry(invoke),
  serveInvoke: (invoke) => membrane.serveInvoke(invoke),
  serveServer: (invoke) =>
    membrane.serveServer(http.createServer((req, res) => invoke(req, res, { waitUntil: () => {} }))),
};

async function start(door: string, invoke: Invoke): Promise<number> {
  const before = messages.filter((m) => m.type === "server-ready").length;
  await doors[door]!(invoke);
  await waitFor(() => messages.filter((m) => m.type === "server-ready").length > before);
  return messages.filter((m) => m.type === "server-ready").at(-1)!.payload.httpPort;
}

async function reach(port: number, headers: Record<string, string>): Promise<number> {
  return new Promise<number>((resolve, reject) => {
    const req = http.request({ host: "127.0.0.1", port, path: "/", headers }, (res) => {
      res.on("data", () => {});
      res.on("end", () => resolve(res.statusCode!));
    });
    req.on("error", reject);
    req.end();
  });
}

let seen: http.IncomingHttpHeaders | undefined;

const echo: Invoke = (req, res) => {
  seen = { ...req.headers };
  res.end("ok");
};

beforeAll(async () => {
  sockDir = await mkdtemp(join(tmpdir(), "ocel-origin-secret-"));
  const sockPath = join(sockDir, "control.sock");

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

  process.env.OCEL_CONTROL_SOCKET = sockPath;
  membrane = await import("../src/shared/membrane.mts");
});

afterEach(() => {
  delete process.env.OCEL_ORIGIN_ROUTER;
  delete process.env.OCEL_ORIGIN_SECRET;
  delete process.env.OCEL_ORIGIN_SIGNED;
  seen = undefined;
});

afterAll(async () => {
  for (const c of controlConns) c.end();
  await new Promise<void>((resolve) => controlServer.close(() => resolve()));
  await rm(sockDir, { recursive: true, force: true });
});

describe.each(Object.keys(doors))("%s", (door) => {
  test("a request carrying no secret never reaches the app", async () => {
    process.env.OCEL_ORIGIN_ROUTER = "1";
    process.env.OCEL_ORIGIN_SECRET = SECRET;
    const port = await start(door, echo);

    expect(await reach(port, {})).toBe(403);
    expect(seen).toBeUndefined();
  });

  test("a request carrying the wrong secret never reaches the app", async () => {
    process.env.OCEL_ORIGIN_ROUTER = "1";
    process.env.OCEL_ORIGIN_SECRET = SECRET;
    const port = await start(door, echo);

    expect(await reach(port, { "x-ocel-origin-secret": SECRET.replace("9", "0") })).toBe(403);
    expect(await reach(port, { "x-ocel-origin-secret": SECRET + "0" })).toBe(403);
    expect(seen).toBeUndefined();
  });

  test("a refused request still completes the invocation", async () => {
    process.env.OCEL_ORIGIN_ROUTER = "1";
    process.env.OCEL_ORIGIN_SECRET = SECRET;
    const port = await start(door, echo);
    const before = messages.filter((m) => m.type === "invocation-complete").length;

    expect(await reach(port, {})).toBe(403);

    await waitFor(() => messages.filter((m) => m.type === "invocation-complete").length > before);
  });

  test("a request carrying the secret is served, and the app never sees it", async () => {
    process.env.OCEL_ORIGIN_ROUTER = "1";
    process.env.OCEL_ORIGIN_SECRET = SECRET;
    const port = await start(door, echo);

    expect(await reach(port, { "x-ocel-origin-secret": SECRET })).toBe(200);
    expect(seen?.["x-ocel-origin-secret"]).toBeUndefined();
  });

  test("the secret leaves the environment the app can read", async () => {
    process.env.OCEL_ORIGIN_ROUTER = "1";
    process.env.OCEL_ORIGIN_SECRET = SECRET;
    await start(door, echo);

    expect(process.env.OCEL_ORIGIN_SECRET).toBeUndefined();
  });

  test("behind an edge that hosts the router itself every request is served", async () => {
    const port = await start(door, echo);

    expect(await reach(port, {})).toBe(200);
    expect(await reach(port, { "x-ocel-origin-secret": "forged" })).toBe(200);
    expect(seen?.["x-ocel-origin-secret"]).toBeUndefined();
  });

  test("a sibling the entry signs to is served without a secret", async () => {
    process.env.OCEL_ORIGIN_ROUTER = "1";
    process.env.OCEL_ORIGIN_SIGNED = "1";
    const port = await start(door, echo);

    expect(await reach(port, {})).toBe(200);
    expect(seen?.["x-ocel-origin-secret"]).toBeUndefined();
  });

  test("an unsigned front door with no secret to demand refuses everything", async () => {
    process.env.OCEL_ORIGIN_ROUTER = "1";
    const port = await start(door, echo);

    expect(await reach(port, {})).toBe(403);
    expect(await reach(port, { "x-ocel-origin-secret": SECRET })).toBe(403);
    expect(seen).toBeUndefined();
  });
});
