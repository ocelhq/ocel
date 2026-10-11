import { EventEmitter } from "node:events";
import { mkdtemp, rm } from "node:fs/promises";
import http from "node:http";
import net from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { Duplex } from "node:stream";
import { hashPreviewPassword, type PreviewGate } from "@platform/edge-contract/preview-gate";
import { afterAll, afterEach, beforeAll, describe, expect, test } from "vitest";

const KEY = "origin-gate-key";
const PASSWORD = "open sesame";
const BYPASS = "ci-bypass-secret";

type Msg = { type: string; payload: any };

const messages: Msg[] = [];
const bus = new EventEmitter();
const controlConns = new Set<net.Socket>();
let controlServer: net.Server;
let sockDir: string;

type Host = typeof import("../src/host.mjs");
type Invoke = import("../src/host.mjs").Invoke;

let host: Host;
let gate: PreviewGate;

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
  serveEntry: (invoke) => host.serveEntry(invoke),
  serveInvoke: (invoke) => host.serveInvoke(invoke),
  serveServer: (invoke) =>
    host.serveServer(http.createServer((req, res) => invoke(req, res, { waitUntil: () => {} }))),
};

async function start(door: string, invoke: Invoke): Promise<number> {
  const before = messages.filter((m) => m.type === "server-ready").length;
  await doors[door]!(invoke);
  await waitFor(() => messages.filter((m) => m.type === "server-ready").length > before);
  return messages.filter((m) => m.type === "server-ready").at(-1)!.payload.httpPort;
}

interface Answer {
  status: number;
  headers: http.IncomingHttpHeaders;
  body: string;
}

function reach(
  port: number,
  options: { method?: string; path?: string; headers?: Record<string, string>; body?: string } = {},
): Promise<Answer> {
  return new Promise<Answer>((resolve, reject) => {
    const req = http.request(
      {
        host: "127.0.0.1",
        port,
        method: options.method ?? "GET",
        path: options.path ?? "/",
        headers: options.headers,
      },
      (res) => {
        let body = "";
        res.on("data", (chunk) => {
          body += chunk;
        });
        res.on("end", () => resolve({ status: res.statusCode!, headers: res.headers, body }));
      },
    );
    req.on("error", reject);
    req.end(options.body);
  });
}

const basic = (password: string): string => `Basic ${btoa(`ocel:${password}`)}`;

let seen: http.IncomingHttpHeaders | undefined;
let reached = 0;

const echo: Invoke = (req, res) => {
  reached++;
  seen = { ...req.headers };
  res.end("served");
};

function gateIs(over: Partial<PreviewGate> = {}): void {
  process.env.OCEL_PREVIEW_GATE = JSON.stringify({ ...gate, ...over });
}

beforeAll(async () => {
  gate = {
    key: KEY,
    passwordMac: await hashPreviewPassword(KEY, PASSWORD),
    bypassSecrets: [BYPASS],
    allowOptions: ["/api"],
  };
  sockDir = await mkdtemp(join(tmpdir(), "ocel-preview-gate-"));
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
  host = await import("../src/host.mjs");
});

afterEach(() => {
  delete process.env.OCEL_PREVIEW_GATE;
  delete process.env.OCEL_ORIGIN_DISPATCH;
  delete process.env.OCEL_ORIGIN_SECRET;
  seen = undefined;
  reached = 0;
});

afterAll(async () => {
  for (const c of controlConns) c.end();
  await new Promise<void>((resolve) => controlServer.close(() => resolve()));
  await rm(sockDir, { recursive: true, force: true });
});

describe.each(Object.keys(doors))("%s", (door) => {
  test("with no gate recorded every request is served as it arrives", async () => {
    const port = await start(door, echo);

    const answer = await reach(port, { headers: { authorization: basic("anything") } });

    expect(answer.status).toBe(200);
    expect(answer.headers["x-robots-tag"]).toBeUndefined();
    expect(seen?.authorization).toBe(basic("anything"));
  });

  test("a request with no credentials is refused with a login form no cache keeps", async () => {
    gateIs();
    const port = await start(door, echo);

    const answer = await reach(port, { path: "/orders?id=7" });

    expect(answer.status).toBe(401);
    expect(answer.headers["cache-control"]).toBe("private, no-store");
    expect(answer.headers["x-robots-tag"]).toBe("noindex");
    expect(answer.body).toContain("/.ocel/preview/login");
    expect(reached).toBe(0);
  });

  test("a refused request still completes the invocation", async () => {
    gateIs();
    const port = await start(door, echo);
    const before = messages.filter((m) => m.type === "invocation-complete").length;

    expect((await reach(port)).status).toBe(401);

    await waitFor(() => messages.filter((m) => m.type === "invocation-complete").length > before);
  });

  test("Basic credentials with the password are served, and the app never sees them", async () => {
    gateIs();
    const port = await start(door, echo);

    const answer = await reach(port, { headers: { authorization: basic(PASSWORD) } });

    expect(answer.status).toBe(200);
    expect(answer.body).toBe("served");
    expect(answer.headers["x-robots-tag"]).toBe("noindex");
    expect(seen?.authorization).toBeUndefined();
  });

  test("Basic credentials with another password are refused", async () => {
    gateIs();
    const port = await start(door, echo);

    expect((await reach(port, { headers: { authorization: basic("guess") } })).status).toBe(401);
    expect(reached).toBe(0);
  });

  test("a bearer token the app checks itself passes through untouched", async () => {
    gateIs();
    const port = await start(door, echo);

    const answer = await reach(port, {
      headers: { authorization: "Bearer app-token", "x-ocel-preview-bypass": BYPASS },
    });

    expect(answer.status).toBe(200);
    expect(seen?.authorization).toBe("Bearer app-token");
    expect(seen?.["x-ocel-preview-bypass"]).toBeUndefined();
  });

  test("the bypass header is served, and the app never sees it", async () => {
    gateIs();
    const port = await start(door, echo);

    const answer = await reach(port, { headers: { "x-ocel-preview-bypass": BYPASS } });

    expect(answer.status).toBe(200);
    expect(seen?.["x-ocel-preview-bypass"]).toBeUndefined();
  });

  test("a wrong bypass secret is refused", async () => {
    gateIs();
    const port = await start(door, echo);

    expect((await reach(port, { headers: { "x-ocel-preview-bypass": "nope" } })).status).toBe(401);
  });

  test("the password posted to the login path answers a session cookie that opens the preview", async () => {
    gateIs();
    const port = await start(door, echo);

    const login = await reach(port, {
      method: "POST",
      path: "/.ocel/preview/login",
      headers: { "content-type": "application/x-www-form-urlencoded" },
      body: `password=${encodeURIComponent(PASSWORD)}&next=%2Forders`,
    });
    const cookie = String(login.headers["set-cookie"]?.[0]).split(";", 1)[0]!;

    expect(login.status).toBe(303);
    expect(login.headers.location).toBe("/orders");
    expect(login.headers["cache-control"]).toBe("private, no-store");
    expect(cookie).toMatch(/^__Host-ocel-preview=/);
    expect(reached).toBe(0);

    const opened = await reach(port, {
      path: "/orders",
      headers: { cookie: `${cookie}; theme=dark` },
    });

    expect(opened.status).toBe(200);
    expect(seen?.cookie).toBe("theme=dark");
  });

  test("a session cookie opens only the host it was issued on, whatever host the client says it forwarded", async () => {
    gateIs();
    const port = await start(door, echo);

    const login = await reach(port, {
      method: "POST",
      path: "/.ocel/preview/login",
      headers: {
        host: "pr-1.preview.example",
        "content-type": "application/x-www-form-urlencoded",
      },
      body: `password=${encodeURIComponent(PASSWORD)}&next=%2F`,
    });
    const cookie = String(login.headers["set-cookie"]?.[0]).split(";", 1)[0]!;

    const replayed = await reach(port, {
      headers: {
        host: "pr-2.preview.example",
        "x-forwarded-host": "pr-1.preview.example",
        cookie,
      },
    });

    expect(replayed.status).toBe(401);
    expect(reached).toBe(0);
  });

  test("a login body past one kilobyte is refused before it is read in full", async () => {
    gateIs();
    const port = await start(door, echo);

    const login = await reach(port, {
      method: "POST",
      path: "/.ocel/preview/login",
      headers: { "content-type": "application/x-www-form-urlencoded" },
      body: `password=${"a".repeat(2048)}`,
    });

    expect(login.status).toBe(413);
    expect(reached).toBe(0);
  });

  test("a preflight to an allowed path is served without credentials", async () => {
    gateIs();
    const port = await start(door, echo);

    expect((await reach(port, { method: "OPTIONS", path: "/api/orders" })).status).toBe(200);
    expect((await reach(port, { method: "OPTIONS", path: "/orders" })).status).toBe(401);
  });

  test("headers an edge sets, sent by a client, open nothing", async () => {
    gateIs();
    const port = await start(door, echo);

    const answer = await reach(port, {
      headers: {
        "x-ocel-edge-cache": "1",
        "x-ocel-entry": "1",
        "x-ocel-request-id": "forged",
        "x-ocel-preview-gated": "1",
        "x-forwarded-proto": "https",
      },
    });

    expect(answer.status).toBe(401);
    expect(reached).toBe(0);
  });

  test("an admitted response keeps every cache header the app set", async () => {
    gateIs();
    const port = await start(door, (_req, res) => {
      res.setHeader("cache-control", "public, max-age=60, s-maxage=300, stale-while-revalidate=30");
      res.setHeader("cdn-cache-control", "max-age=300");
      res.setHeader("cloudflare-cdn-cache-control", "max-age=300");
      res.setHeader("surrogate-control", "max-age=300");
      res.end("page");
    });

    const answer = await reach(port, { headers: { authorization: basic(PASSWORD) } });

    expect(answer.status).toBe(200);
    expect(answer.headers["cache-control"]).toBe(
      "public, max-age=60, s-maxage=300, stale-while-revalidate=30",
    );
    expect(answer.headers["cdn-cache-control"]).toBe("max-age=300");
    expect(answer.headers["cloudflare-cdn-cache-control"]).toBe("max-age=300");
    expect(answer.headers["surrogate-control"]).toBe("max-age=300");
  });

  test("an admitted response the app gave no cache policy is given none", async () => {
    gateIs();
    const port = await start(door, echo);

    const answer = await reach(port, { headers: { authorization: basic(PASSWORD) } });

    expect(answer.headers["cache-control"]).toBeUndefined();
  });

  test("the app cannot take the noindex mark off a response", async () => {
    gateIs();
    const port = await start(door, (_req, res) => {
      res.setHeader("x-robots-tag", "all");
      res.end("page");
    });

    const answer = await reach(port, { headers: { authorization: basic(PASSWORD) } });

    expect(answer.headers["x-robots-tag"]).toBe("noindex");
  });

  test("keys that cannot be read refuse every request instead of opening the preview", async () => {
    process.env.OCEL_PREVIEW_GATE = "{not json";
    const port = await start(door, echo);

    const answer = await reach(port, { headers: { authorization: basic(PASSWORD) } });

    expect(answer.status).toBe(503);
    expect(answer.headers["cache-control"]).toBe("private, no-store");
    expect(answer.headers["x-robots-tag"]).toBe("noindex");
    expect(reached).toBe(0);
  });

  test("the keys leave the environment the app can read", async () => {
    gateIs();
    await start(door, echo);

    expect(process.env.OCEL_PREVIEW_GATE).toBeUndefined();
  });

  test("the origin secret is checked before the gate, so a request without it never sees the login form", async () => {
    gateIs();
    process.env.OCEL_ORIGIN_DISPATCH = "1";
    process.env.OCEL_ORIGIN_SECRET = "s3cret-value";
    const port = await start(door, echo);

    expect((await reach(port)).status).toBe(403);
    expect(
      (await reach(port, { headers: { "x-ocel-origin-secret": "s3cret-value" } })).status,
    ).toBe(401);
  });
});

describe("serveLocal", () => {
  test("the loopback an entry dispatches to is not gated again", async () => {
    gateIs();
    const before = messages.filter((m) => m.type === "server-ready").length;
    const port = await host.serveLocal(echo);

    const answer = await reach(port);

    expect(answer.status).toBe(200);
    expect(messages.filter((m) => m.type === "server-ready").length).toBe(before);
    expect(process.env.OCEL_PREVIEW_GATE).toBeDefined();
  });
});

describe("serveServer upgrades", () => {
  let upgraded = 0;
  let seenUpgrade: http.IncomingHttpHeaders | undefined;

  const acceptUpgrade = (req: http.IncomingMessage, socket: Duplex): void => {
    upgraded++;
    seenUpgrade = { ...req.headers };
    socket.end(
      "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n",
    );
  };

  afterEach(() => {
    upgraded = 0;
    seenUpgrade = undefined;
  });

  async function startUpgrading(registered: "before" | "after"): Promise<number> {
    const server = http.createServer((_req, res) => res.end("served"));
    if (registered === "before") server.on("upgrade", acceptUpgrade);
    const before = messages.filter((m) => m.type === "server-ready").length;
    await host.serveServer(server);
    if (registered === "after") server.on("upgrade", acceptUpgrade);
    await waitFor(() => messages.filter((m) => m.type === "server-ready").length > before);
    return messages.filter((m) => m.type === "server-ready").at(-1)!.payload.httpPort;
  }

  function reachUpgrade(port: number, headers: Record<string, string> = {}): Promise<number> {
    return new Promise<number>((resolve, reject) => {
      const req = http.request({
        host: "127.0.0.1",
        port,
        path: "/socket",
        headers: { connection: "Upgrade", upgrade: "websocket", ...headers },
      });
      req.on("upgrade", (res, socket) => {
        socket.destroy();
        resolve(res.statusCode!);
      });
      req.on("response", (res) => {
        res.resume();
        resolve(res.statusCode!);
      });
      req.on("error", reject);
      req.end();
    });
  }

  test.each(["before", "after"] as const)(
    "an upgrade without credentials is refused before a listener registered %s serving sees it",
    async (registered) => {
      gateIs();
      const port = await startUpgrading(registered);

      expect(await reachUpgrade(port)).toBe(401);
      expect(upgraded).toBe(0);
    },
  );

  test("an upgrade with the password reaches the app, which never sees the credentials", async () => {
    gateIs();
    const port = await startUpgrading("before");

    expect(await reachUpgrade(port, { authorization: basic(PASSWORD) })).toBe(101);
    expect(upgraded).toBe(1);
    expect(seenUpgrade?.authorization).toBeUndefined();
  });

  test("an upgrade without the origin secret is refused before the gate", async () => {
    gateIs();
    process.env.OCEL_ORIGIN_DISPATCH = "1";
    process.env.OCEL_ORIGIN_SECRET = "s3cret-value";
    const port = await startUpgrading("before");

    expect(await reachUpgrade(port, { authorization: basic(PASSWORD) })).toBe(403);
    expect(upgraded).toBe(0);
  });
});
