import { EventEmitter } from "node:events";
import { mkdtemp as makeTempDir, mkdtemp, rm, writeFile } from "node:fs/promises";
import http from "node:http";
import net from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  GATE_FILE_VAR,
  GATE_INLINE_VAR,
  hashPassword,
  hashRevalidateToken,
} from "@platform/edge-contract/gate";
import { afterAll, afterEach, beforeAll, describe, expect, test } from "vitest";

type Msg = { type: string; payload: any };

const messages: Msg[] = [];
const bus = new EventEmitter();
const controlConns = new Set<net.Socket>();
let controlServer: net.Server;
let sockDir: string;

type Host = typeof import("../src/host.mjs");
type Invoke = import("../src/host.mjs").Invoke;

let host: Host;

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

async function reach(
  port: number,
  headers: Record<string, string>,
  method = "GET",
): Promise<number> {
  return new Promise<number>((resolve, reject) => {
    const req = http.request({ host: "127.0.0.1", port, path: "/", headers, method }, (res) => {
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
  sockDir = await mkdtemp(join(tmpdir(), "ocel-gate-"));
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
  delete process.env[GATE_FILE_VAR];
  delete process.env[GATE_INLINE_VAR];
  seen = undefined;
});

afterAll(async () => {
  for (const c of controlConns) c.end();
  await new Promise<void>((resolve) => controlServer.close(() => resolve()));
  await rm(sockDir, { recursive: true, force: true });
});

async function mountGate(contents: string): Promise<string> {
  const dir = await makeTempDir(join(tmpdir(), "ocel-mounted-gate-"));
  const path = join(dir, "gate");
  await writeFile(path, contents);
  return path;
}

const signedIn = { authorization: `Basic ${Buffer.from("anyone:pw").toString("base64")}` };

describe.each(Object.keys(doors))("%s", (door) => {
  test("a request with no credentials never reaches the app", async () => {
    const key = "k";
    const mac = await hashPassword(key, "pw");
    process.env[GATE_FILE_VAR] = await mountGate(JSON.stringify({ key, passwordMac: mac }));
    const port = await start(door, echo);

    expect(await reach(port, {})).toBe(401);
    expect(seen).toBeUndefined();
  });

  test("a request with the password is served, and the app never sees it", async () => {
    const mac = await hashPassword("k", "pw");
    process.env[GATE_FILE_VAR] = await mountGate(JSON.stringify({ key: "k", passwordMac: mac }));
    const port = await start(door, echo);

    expect(await reach(port, signedIn)).toBe(200);
    expect(seen?.authorization).toBeUndefined();
  });

  test("an admitted request still has the control headers a visitor sent stripped", async () => {
    const mac = await hashPassword("k", "pw");
    process.env[GATE_FILE_VAR] = await mountGate(JSON.stringify({ key: "k", passwordMac: mac }));
    const port = await start(door, echo);

    expect(
      await reach(port, {
        ...signedIn,
        "x-ocel-entry": "spoofed",
        "x-ocel-trace-id": "spoofed",
        "x-ocel-origin-secret": "spoofed",
      }),
    ).toBe(200);
    expect(seen?.["x-ocel-entry"]).toBeUndefined();
    expect(seen?.["x-ocel-trace-id"]).toBeUndefined();
    expect(seen?.["x-ocel-origin-secret"]).toBeUndefined();
  });

  test("an admitted request keeps the host the runtime settled on", async () => {
    const mac = await hashPassword("k", "pw");
    process.env[GATE_FILE_VAR] = await mountGate(JSON.stringify({ key: "k", passwordMac: mac }));
    const port = await start(door, echo);

    expect(
      await reach(port, {
        ...signedIn,
        "x-forwarded-host": "forwarded.example",
        "x-forwarded-proto": "https",
      }),
    ).toBe(200);
    if (door === "serveEntry") {
      expect(seen?.host).toBe(`127.0.0.1:${port}`);
      expect(seen?.["x-forwarded-host"]).toBeUndefined();
      expect(seen?.["x-forwarded-proto"]).toBeUndefined();
    } else {
      expect(seen?.host).toBe("forwarded.example");
    }
  });

  test("where the gate lives leaves the environment the app can read", async () => {
    process.env[GATE_FILE_VAR] = await mountGate("null");
    process.env[GATE_INLINE_VAR] = "null";
    await start(door, echo);

    expect(process.env[GATE_FILE_VAR]).toBeUndefined();
    expect(process.env[GATE_INLINE_VAR]).toBeUndefined();
  });

  test("a gate that cannot be read serves no one", async () => {
    process.env[GATE_FILE_VAR] = join(tmpdir(), "ocel-no-such-gate");
    const port = await start(door, echo);

    expect(await reach(port, signedIn)).toBe(503);
    expect(seen).toBeUndefined();
  });

  test("a service with no mounted gate is served to anyone", async () => {
    const port = await start(door, echo);

    expect(await reach(port, {})).toBe(200);
  });
});

describe.each(["serveEntry", "serveInvoke"] as const)(
  "%s given the release's revalidate token digest",
  (door) => {
    const token = "b1946ac92492d2347c6235b4d2611184";

    async function startRevalidating(): Promise<number> {
      const mac = await hashPassword("k", "pw");
      process.env[GATE_FILE_VAR] = await mountGate(JSON.stringify({ key: "k", passwordMac: mac }));
      const digest = await hashRevalidateToken(token);
      const before = messages.filter((m) => m.type === "server-ready").length;
      await (door === "serveEntry"
        ? host.serveEntry(echo, undefined, digest)
        : host.serveInvoke(echo, undefined, undefined, digest));
      await waitFor(() => messages.filter((m) => m.type === "server-ready").length > before);
      return messages.filter((m) => m.type === "server-ready").at(-1)!.payload.httpPort;
    }

    test("a HEAD carrying the token reaches the app with the token kept", async () => {
      const port = await startRevalidating();

      expect(await reach(port, { "x-prerender-revalidate": token }, "HEAD")).toBe(200);
      expect(seen?.["x-prerender-revalidate"]).toBe(token);
    });

    test("a GET carrying the token never reaches the app", async () => {
      const port = await startRevalidating();

      expect(await reach(port, { "x-prerender-revalidate": token })).toBe(401);
      expect(seen).toBeUndefined();
    });
  },
);
