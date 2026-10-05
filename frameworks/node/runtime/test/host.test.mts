import { EventEmitter } from "node:events";
import { mkdtemp, rm } from "node:fs/promises";
import http from "node:http";
import net from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, describe, expect, test } from "vitest";
import { background, runWithWaitUntil } from "../src/background.mjs";

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

let serveInvoke: typeof import("../src/host.mjs").serveInvoke;
let serveServer: typeof import("../src/host.mjs").serveServer;
let drainWaitUntil: typeof import("../src/host.mjs").drainWaitUntil;
let startServer: typeof import("../src/host.mjs").startServer;
type Invoke = import("../src/host.mjs").Invoke;

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
  const mod = await import("../src/host.mjs");
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
      (_req, res) => {
        res.end("ok");
      },
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
  test("delays invocation-complete until waitUntil finishes, after request-end", async () => {
    const events: string[] = [];
    const invoke: Invoke = (_req, res, ocel) => {
      ocel.waitUntil(
        new Promise<void>((r) =>
          setTimeout(() => {
            events.push("waitUntil-finished");
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
    expect(events).toContain("waitUntil-finished");
  });

  test("still completes when the request is aborted (close without finish)", async () => {
    let drained = false;
    const invoke: Invoke = (_req, res, ocel) => {
      ocel.waitUntil(
        new Promise<void>((r) =>
          setTimeout(() => {
            drained = true;
            r();
          }, 60),
        ),
      );
      setTimeout(() => {
        try {
          res.end("late");
        } catch {}
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
      messages.some((m) => m.type === "invocation-complete" && m.payload.requestId === "req-abort"),
    );
    expect(drained).toBe(true);
  });

  test("keeps the invocation open for work deferred through the background bridge", async () => {
    let drained = false;
    const invoke: Invoke = (_req, res, ocel) =>
      runWithWaitUntil(ocel.waitUntil, async () => {
        await Promise.resolve();
        background(
          () =>
            new Promise<void>((r) =>
              setTimeout(() => {
                drained = true;
                r();
              }, 80),
            ),
        );
        res.end("ok");
      });

    const port = await start(invoke);

    await request(port, "req-bridge");
    expect(drained).toBe(false);

    await waitFor(() =>
      messages.some(
        (m) => m.type === "invocation-complete" && m.payload.requestId === "req-bridge",
      ),
    );
    expect(drained).toBe(true);
  });
});

describe("a host whose CPU stops when the response ends", () => {
  function settlingAfter(ms: number, done: () => void): Promise<void> {
    return new Promise<void>((r) =>
      setTimeout(() => {
        done();
        r();
      }, ms),
    );
  }

  function holdingFor(ms: number, done: () => void): Invoke {
    return (_req, res, ocel) =>
      runWithWaitUntil(ocel.holdEnd, async () => {
        background(() => settlingAfter(ms, done));
        res.end("ok");
      });
  }

  async function startDeclaring(capMs: string | undefined, invoke: Invoke): Promise<number> {
    if (capMs === undefined) delete process.env.OCEL_FINISH_BEFORE_RESPONSE_MS;
    else process.env.OCEL_FINISH_BEFORE_RESPONSE_MS = capMs;
    try {
      return await start(invoke);
    } finally {
      delete process.env.OCEL_FINISH_BEFORE_RESPONSE_MS;
    }
  }

  test("holds the last byte until the runtime's own background work settles", async () => {
    let settled = false;
    const port = await startDeclaring(
      "5000",
      holdingFor(80, () => {
        settled = true;
      }),
    );

    await request(port, "req-finish");

    expect(settled).toBe(true);
  });

  test("holds the last byte for work that work it held defers in turn", async () => {
    let settled = false;
    const port = await startDeclaring("5000", (_req, res, ocel) =>
      runWithWaitUntil(ocel.holdEnd, async () => {
        background(async () => {
          await Promise.resolve();
          background(() =>
            settlingAfter(60, () => {
              settled = true;
            }),
          );
        });
        res.end("ok");
      }),
    );

    await request(port, "req-finish-nested");

    expect(settled).toBe(true);
  });

  test("holds the last byte for work registered as the end writes the response's head", async () => {
    let settled = false;
    const port = await startDeclaring("5000", (_req, res, ocel) => {
      const writeHead = res.writeHead;
      res.writeHead = function (this: http.ServerResponse, ...args: any[]) {
        ocel.holdEnd(
          settlingAfter(80, () => {
            settled = true;
          }),
        );
        return (writeHead as any).apply(this, args);
      } as typeof res.writeHead;
      res.end("ok");
    });

    const res = await fetch(`http://127.0.0.1:${port}/`);
    const body = await res.text();

    expect(settled).toBe(true);
    expect(body).toBe("ok");
    expect(res.headers.get("content-length")).toBe("2");
  });

  test("does not hold the last byte for app work that settles only once the response closes", async () => {
    const port = await startDeclaring("5000", (_req, res, ocel) => {
      ocel.waitUntil(new Promise((resolve) => res.once("close", resolve)));
      res.end("ok");
    });
    const started = performance.now();

    await request(port, "req-after-close");

    expect(performance.now() - started).toBeLessThan(1_000);
  });

  test("a second end while the first is held does not send the last byte early", async () => {
    let settled = false;
    const port = await startDeclaring("5000", (_req, res, ocel) => {
      ocel.holdEnd(
        settlingAfter(80, () => {
          settled = true;
        }),
      );
      res.end("ok");
      res.end();
    });

    const body = await requestBody(port, "req-second-end");

    expect(settled).toBe(true);
    expect(body).toBe("ok");
  });

  test("holds the last byte of a body whose length the app declared and wrote before ending", async () => {
    let settled = false;
    const port = await startDeclaring("5000", (_req, res, ocel) => {
      ocel.holdEnd(
        settlingAfter(80, () => {
          settled = true;
        }),
      );
      res.setHeader("content-length", "4");
      res.write("ok");
      res.write("ok");
      res.end();
    });

    const body = await requestBody(port, "req-sized");

    expect(settled).toBe(true);
    expect(body).toBe("okok");
  });

  test("holds the last byte of a body whose length the app declared through writeHead", async () => {
    let settled = false;
    const port = await startDeclaring("5000", (_req, res, ocel) => {
      ocel.holdEnd(
        settlingAfter(80, () => {
          settled = true;
        }),
      );
      res.writeHead(200, { "content-length": "4" });
      res.write("okok");
      res.end();
    });

    const body = await requestBody(port, "req-sized-head");

    expect(settled).toBe(true);
    expect(body).toBe("okok");
  });

  test("sends every write that follows the one completing a declared body, and calls its callback", async () => {
    let settled = false;
    let emptyWriteDone = false;
    const port = await startDeclaring("5000", (_req, res, ocel) => {
      ocel.holdEnd(
        settlingAfter(80, () => {
          settled = true;
        }),
      );
      res.setHeader("content-length", "4");
      res.write("okok");
      res.write("", () => {
        emptyWriteDone = true;
      });
      res.end();
    });

    const body = await requestBody(port, "req-sized-empty-write");

    expect(settled).toBe(true);
    expect(body).toBe("okok");
    expect(emptyWriteDone).toBe(true);
  });

  test("a second end while the first is held calls its callback once the response finishes", async () => {
    let settledBeforeCallback: boolean | undefined;
    let settled = false;
    let finished = false;
    const port = await startDeclaring("5000", (_req, res, ocel) => {
      ocel.holdEnd(
        settlingAfter(80, () => {
          settled = true;
        }),
      );
      res.end("ok");
      res.end(() => {
        settledBeforeCallback = settled;
        finished = res.writableFinished;
      });
    });

    await requestBody(port, "req-second-end-callback");
    await new Promise((r) => setTimeout(r, 20));

    expect(settledBeforeCallback).toBe(true);
    expect(finished).toBe(true);
  });

  test("a write after a held end fails as a write after end does", async () => {
    let failure: (Error & { code?: string }) | null | undefined;
    const port = await startDeclaring("5000", (_req, res, ocel) => {
      ocel.holdEnd(settlingAfter(40, () => {}));
      res.on("error", () => {});
      res.end("ok");
      res.write("late", (err) => {
        failure = err;
      });
    });

    const body = await requestBody(port, "req-write-after-held-end");
    await new Promise((r) => setTimeout(r, 20));

    expect(body).toBe("ok");
    expect(failure?.code).toBe("ERR_STREAM_WRITE_AFTER_END");
  });

  test("ends the response at its cap when held work outlasts it", async () => {
    const port = await startDeclaring("50", (_req, res, ocel) => {
      ocel.holdEnd(new Promise(() => {}));
      res.end("ok");
    });
    const started = performance.now();

    await request(port, "req-capped");

    expect(performance.now() - started).toBeLessThan(2_000);
  });

  test("completes the invocation only once held work that outlasted the cap settles", async () => {
    let settled = false;
    const port = await startDeclaring("30", (_req, res, ocel) => {
      ocel.holdEnd(
        settlingAfter(200, () => {
          settled = true;
        }),
      );
      res.end("ok");
    });

    await request(port, "req-capped-complete");
    await waitFor(() =>
      messages.some(
        (m) => m.type === "invocation-complete" && m.payload.requestId === "req-capped-complete",
      ),
    );

    expect(settled).toBe(true);
  });

  test("ends the response first on a host that declares no such thing", async () => {
    let settled = false;
    const port = await startDeclaring(
      undefined,
      holdingFor(80, () => {
        settled = true;
      }),
    );

    await request(port, "req-unheld");

    expect(settled).toBe(false);
  });
});

describe("an app that calls listen() instead of exporting a handler", () => {
  async function startServed(server: http.Server): Promise<number> {
    const before = messages.filter((m) => m.type === "server-ready").length;
    await serveServer(server);
    await waitFor(() => messages.filter((m) => m.type === "server-ready").length > before);
    return messages.filter((m) => m.type === "server-ready").at(-1)!.payload.httpPort;
  }

  test("completes its invocation instead of leaving the runtime waiting out the budget", async () => {
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

  test("does not leak the runtime's own headers into the app", async () => {
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

  test("routes once, prepend and removal through the lifted listeners too", async () => {
    const ran: string[] = [];
    const server = http.createServer();
    const removed = (): void => void ran.push("removed");
    const port = await startServed(server);

    server.on("request", () => ran.push("on"));
    server.once("request", () => ran.push("once"));
    server.prependListener("request", () => ran.push("prepended"));
    server.on("request", removed);
    server.off("request", removed);
    server.on("request", (req, res) => {
      ran.push(String(req.headers["x-ocel-request-id"]));
      res.end("ok");
    });

    await request(port, "listen-once-a");
    await request(port, "listen-once-b");

    expect(ran).toEqual(["prepended", "on", "once", "undefined", "prepended", "on", "undefined"]);

    server.close();
  });

  test("declares that it can signal the invocation lifecycle", async () => {
    const server = http.createServer((_req, res) => res.end("ok"));
    await startServed(server);

    const ready = messages.filter((m) => m.type === "server-ready").at(-1)!;
    expect(ready.payload.lifecycle).toBe(true);

    server.close();
  });

  test("a bare startServer declares that it cannot, so the runtime never waits on it", async () => {
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

function requestBody(port: number, requestId: string): Promise<string> {
  return new Promise<string>((resolve, reject) => {
    const req = http.request(
      { host: "127.0.0.1", port, path: "/", headers: { "x-ocel-request-id": requestId } },
      (res) => {
        let body = "";
        res.on("data", (chunk) => {
          body += chunk;
        });
        res.on("end", () => resolve(body));
      },
    );
    req.on("error", reject);
    req.end();
  });
}
