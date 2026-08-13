import net from "node:net";
import http from "node:http";
import { EventEmitter } from "node:events";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, describe, expect, test } from "vitest";
import { background, runWithWaitUntil } from "../src/shared/background.mjs";

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

async function start(invoke: Invoke): Promise<number> {
  const before = messages.filter((m) => m.type === "server-ready").length;
  await serveInvoke(invoke);
  await waitFor(() => messages.filter((m) => m.type === "server-ready").length > before);
  return messages.filter((m) => m.type === "server-ready").at(-1)!.payload.httpPort;
}

let serveInvoke: typeof import("../src/shared/membrane.mts").serveInvoke;
let serveServer: typeof import("../src/shared/membrane.mts").serveServer;
let drainWaitUntil: typeof import("../src/shared/membrane.mts").drainWaitUntil;
let startServer: typeof import("../src/shared/membrane.mts").startServer;
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
  serveServer = mod.serveServer;
  drainWaitUntil = mod.drainWaitUntil;
  startServer = mod.startServer;
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
        pending.push(Promise.resolve().then(() => order.push("late")));
      }),
    );

    await drainWaitUntil(pending);

    expect(order).toEqual(["first", "late"]);
    expect(pending).toHaveLength(0);
  });
});

describe("the loopback header boundary", () => {
  async function observe(headers: Record<string, string>): Promise<http.IncomingHttpHeaders> {
    let seen: http.IncomingHttpHeaders | undefined;
    const port = await start((req, res) => {
      seen = { ...req.headers };
      res.end("ok");
    });
    await new Promise<void>((resolve, reject) => {
      const req = http.request({ host: "127.0.0.1", port, path: "/", headers }, (res) => {
        res.on("data", () => {});
        res.on("end", () => resolve());
      });
      req.on("error", reject);
      req.end();
    });
    return seen!;
  }

  test("the app reads the public authority off Host, not the loopback one", async () => {
    const seen = await observe({ "x-forwarded-host": "app.ocel.site" });

    expect(seen.host).toBe("app.ocel.site");
  });

  test("a comma-joined x-forwarded-host uses its leftmost authority", async () => {
    const seen = await observe({ "x-forwarded-host": "app.ocel.site, edge.internal" });

    expect(seen.host).toBe("app.ocel.site");
  });

  test("without x-forwarded-host the authority is left alone", async () => {
    const seen = await observe({});

    expect(seen.host).toMatch(/^127\.0\.0\.1:\d+$/);
  });

  test("a self-fetch loses the entry key it inherited", async () => {
    const seen = await observe({ "x-ocel-entry": "/server" });

    expect(seen["x-ocel-entry"]).toBeUndefined();
  });

  test("a request the bootstrap forwarded keeps its entry key", async () => {
    const seen = await observe({ "x-ocel-entry": "/server", "x-ocel-request-id": "abc" });

    expect(seen["x-ocel-entry"]).toBe("/server");
    expect(seen["x-ocel-request-id"]).toBeUndefined();
  });
});

describe("the loopback server's socket lifetime", () => {
  test("never reaps an idle keep-alive connection on its own", async () => {
    const before = messages.filter((m) => m.type === "server-ready").length;
    const server = http.createServer((_req, res) => res.end("ok"));
    await new Promise<void>((resolve, reject) => {
      server.on("error", reject);
      startServer(server).then(resolve, reject);
    });
    await waitFor(() => messages.filter((m) => m.type === "server-ready").length > before);

    expect(server.keepAliveTimeout).toBe(0);
    expect(server.headersTimeout).toBe(0);

    server.close();
  });
});

describe("onListening", () => {
  test("is handed the port the server bound", async () => {
    const before = messages.filter((m) => m.type === "server-ready").length;
    let seen: number | undefined;

    await serveInvoke(
      (_req, res) => res.end("ok"),
      (port) => {
        seen = port;
      },
    );
    await waitFor(() => messages.filter((m) => m.type === "server-ready").length > before);

    const ready = messages.filter((m) => m.type === "server-ready").at(-1)!;
    expect(seen).toBe(ready.payload.httpPort);
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
    expect(icIdx).toBeGreaterThan(reIdx);
    expect(events).toContain("waitUntil-settled");
  });

  test("still completes when the request is aborted (close without finish)", async () => {
    let settled = false;
    const invoke: Invoke = (_req, res, ocel) => {
      ocel.waitUntil(new Promise<void>((r) => setTimeout(() => ((settled = true), r()), 60)));
      setTimeout(() => {
        try {
          res.end("late");
        } catch {
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

  test("holds the invocation for work deferred through the background bridge", async () => {
    let settled = false;
    const invoke: Invoke = (_req, res, ocel) =>
      runWithWaitUntil(ocel.waitUntil, async () => {
        await Promise.resolve();
        background(
          () => new Promise<void>((r) => setTimeout(() => ((settled = true), r()), 80)),
        );
        res.end("ok");
      });

    const port = await start(invoke);

    await request(port, "req-bridge");
    expect(settled).toBe(false);

    await waitFor(() =>
      messages.some(
        (m) => m.type === "invocation-complete" && m.payload.requestId === "req-bridge",
      ),
    );
    expect(settled).toBe(true);
  });
});

describe("an app that calls listen() instead of exporting a handler", () => {
  async function startServed(server: http.Server): Promise<number> {
    const before = messages.filter((m) => m.type === "server-ready").length;
    await serveServer(server);
    await waitFor(() => messages.filter((m) => m.type === "server-ready").length > before);
    return messages.filter((m) => m.type === "server-ready").at(-1)!.payload.httpPort;
  }

  test("completes its invocation instead of leaving the membrane waiting out the budget", async () => {
    const server = http.createServer((_req, res) => res.end("ok"));
    const port = await startServed(server);

    await request(port, "listen-shaped");

    await waitFor(() =>
      messages.some((m) => m.type === "request-end" && m.payload.requestId === "listen-shaped"),
    );
    await waitFor(() =>
      messages.some(
        (m) => m.type === "invocation-complete" && m.payload.requestId === "listen-shaped",
      ),
    );

    server.close();
  });

  test("does not leak the membrane's own headers into the app", async () => {
    let seen: http.IncomingHttpHeaders | undefined;
    const server = http.createServer((req, res) => {
      seen = { ...req.headers };
      res.end("ok");
    });
    const port = await startServed(server);

    await request(port, "listen-headers");

    expect(seen!["x-ocel-request-id"]).toBeUndefined();
    expect(seen!["x-ocel-trace-id"]).toBeUndefined();

    server.close();
  });

  test("every request listener the app attached still runs, including a late one", async () => {
    const ran: string[] = [];
    const server = http.createServer();
    server.on("request", () => ran.push("first"));
    const port = await startServed(server);
    server.on("request", (_req, res) => {
      ran.push("late");
      res.end("ok");
    });

    await request(port, "listen-listeners");

    expect(ran).toEqual(["first", "late"]);

    server.close();
  });

  test("declares that it can signal the invocation lifecycle", async () => {
    const server = http.createServer((_req, res) => res.end("ok"));
    await startServed(server);

    const ready = messages.filter((m) => m.type === "server-ready").at(-1)!;
    expect(ready.payload.lifecycle).toBe(true);

    server.close();
  });

  test("a bare startServer declares that it cannot, so the membrane never waits on it", async () => {
    const before = messages.filter((m) => m.type === "server-ready").length;
    const server = http.createServer((_req, res) => res.end("ok"));
    await startServer(server);
    await waitFor(() => messages.filter((m) => m.type === "server-ready").length > before);

    const ready = messages.filter((m) => m.type === "server-ready").at(-1)!;
    expect(ready.payload.lifecycle).toBe(false);

    server.close();
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
