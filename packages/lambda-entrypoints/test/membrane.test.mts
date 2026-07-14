import net from "node:net";
import http from "node:http";
import { EventEmitter } from "node:events";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, describe, expect, test } from "vitest";

type Msg = { type: string; payload: any };

const messages: Msg[] = [];
const bus = new EventEmitter();
let controlServer: net.Server;
const controlConns = new Set<net.Socket>();
let sockDir: string;

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

// start serves the invoke and resolves with its port once a *fresh*
// server-ready has arrived over the (async) control socket — waiting avoids
// picking up a previous server's port.
async function start(invoke: Invoke): Promise<number> {
  const before = messages.filter((m) => m.type === "server-ready").length;
  await serveInvoke(invoke);
  await waitFor(() => messages.filter((m) => m.type === "server-ready").length > before);
  return messages.filter((m) => m.type === "server-ready").at(-1)!.payload.httpPort;
}

// Import the membrane only after OCEL_CONTROL_SOCKET is set, so the lazy control
// connection targets our capture socket. Populated in beforeAll.
let serveInvoke: typeof import("../src/shared/membrane.mts").serveInvoke;
let drainWaitUntil: typeof import("../src/shared/membrane.mts").drainWaitUntil;
type Invoke = import("../src/shared/membrane.mts").Invoke;

beforeAll(async () => {
  sockDir = await mkdtemp(join(tmpdir(), "ocel-ctrl-"));
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
  const mod = await import("../src/shared/membrane.mts");
  serveInvoke = mod.serveInvoke;
  drainWaitUntil = mod.drainWaitUntil;
});

afterAll(async () => {
  for (const c of controlConns) c.destroy();
  await new Promise<void>((resolve) => controlServer.close(() => resolve()));
  await rm(sockDir, { recursive: true, force: true });
});

describe("drainWaitUntil", () => {
  test("drains promises registered while draining, in order", async () => {
    const order: string[] = [];
    const pending: Promise<unknown>[] = [];
    pending.push(
      Promise.resolve().then(() => {
        order.push("first");
        // A late registration, added while the first batch is still settling.
        pending.push(Promise.resolve().then(() => order.push("late")));
      }),
    );

    await drainWaitUntil(pending);

    expect(order).toEqual(["first", "late"]);
    expect(pending).toHaveLength(0);
  });
});

describe("invocation lifecycle", () => {
  test("holds invocation-complete until waitUntil settles, after request-end", async () => {
    const events: string[] = [];
    const invoke: Invoke = (_req, res, ocel) => {
      ocel.waitUntil(
        new Promise<void>((r) =>
          setTimeout(() => {
            events.push("waitUntil-settled");
            r();
          }, 120),
        ),
      );
      res.end("ok");
    };

    const port = await start(invoke);

    await request(port, "req-lifecycle");

    await waitFor(() =>
      messages.some(
        (m) => m.type === "invocation-complete" && m.payload.requestId === "req-lifecycle",
      ),
    );

    const reIdx = messages.findIndex(
      (m) => m.type === "request-end" && m.payload.requestId === "req-lifecycle",
    );
    const icIdx = messages.findIndex(
      (m) => m.type === "invocation-complete" && m.payload.requestId === "req-lifecycle",
    );
    expect(reIdx).toBeGreaterThanOrEqual(0);
    // Completion is gated behind telemetry and the background task.
    expect(icIdx).toBeGreaterThan(reIdx);
    expect(events).toContain("waitUntil-settled");
  });

  test("still completes when the request is aborted (close without finish)", async () => {
    let settled = false;
    const invoke: Invoke = (_req, res, ocel) => {
      ocel.waitUntil(new Promise<void>((r) => setTimeout(() => ((settled = true), r()), 60)));
      // Hold the response open so the client's abort lands before finish.
      setTimeout(() => {
        try {
          res.end("late");
        } catch {
          /* connection already gone */
        }
      }, 1000);
    };

    const port = await start(invoke);

    await new Promise<void>((resolve) => {
      const req = http.request(
        { host: "127.0.0.1", port, path: "/", headers: { "x-ocel-request-id": "req-abort" } },
        () => {},
      );
      req.on("error", () => {});
      req.end();
      setTimeout(() => {
        req.destroy();
        resolve();
      }, 50);
    });

    await waitFor(() =>
      messages.some(
        (m) => m.type === "invocation-complete" && m.payload.requestId === "req-abort",
      ),
    );
    expect(settled).toBe(true);
  });
});

function request(port: number, requestId: string): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    const req = http.request(
      { host: "127.0.0.1", port, path: "/", headers: { "x-ocel-request-id": requestId } },
      (res) => {
        res.on("data", () => {});
        res.on("end", () => resolve());
      },
    );
    req.on("error", reject);
    req.end();
  });
}
