import net from "node:net";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, expect, test, vi } from "vitest";

// The node entrypoint boots for real, over a real control socket, the same way
// live-boot.test.mts does — the bug this guards lives in the interaction
// between boot() actually finishing and a warm-compile-cache reply actually
// arriving, which a unit test around a hand-extracted function would miss.
type Msg = { type: string; payload: any };

let dir: string;
let server: net.Server;
let connections: net.Socket[];
let messages: Msg[];

function push(message: unknown): void {
  for (const conn of connections) conn.write(JSON.stringify(message) + "\n");
}

async function waitFor(pred: () => boolean, label: string): Promise<void> {
  for (let i = 0; i < 200; i++) {
    if (pred()) return;
    await new Promise((r) => setTimeout(r, 10));
  }
  throw new Error(`timed out waiting for ${label}; messages so far: ${JSON.stringify(messages)}`);
}

async function warmReply(): Promise<Record<string, unknown>> {
  await waitFor(() => messages.some((m) => m.type === "server-ready"), "server-ready");
  push({ type: "warm-compile-cache", payload: {} });
  await waitFor(
    () => messages.some((m) => m.type === "compile-cache-warmed"),
    "compile-cache-warmed",
  );
  return messages.find((m) => m.type === "compile-cache-warmed")!.payload;
}

beforeEach(async () => {
  connections = [];
  messages = [];
  dir = await mkdtemp(join(tmpdir(), "ocel-node-warm-"));

  server = net.createServer((conn) => {
    connections.push(conn);
    let buffer = "";
    conn.on("data", (chunk) => {
      buffer += chunk.toString();
      let end: number;
      while ((end = buffer.indexOf("\n")) >= 0) {
        const line = buffer.slice(0, end);
        buffer = buffer.slice(end + 1);
        if (line.trim()) messages.push(JSON.parse(line));
      }
    });
  });
  await new Promise<void>((resolve) => server.listen(join(dir, "control.sock"), resolve));

  const handler = join(dir, "app.mjs");
  await writeFile(handler, `export default (req, res) => res.end("ok");\n`);
  vi.stubEnv("OCEL_CONTROL_SOCKET", join(dir, "control.sock"));
  vi.stubEnv("OCEL_HANDLER", handler);
  vi.resetModules();
});

afterEach(async () => {
  for (const conn of connections) conn.destroy();
  await new Promise<void>((resolve) => server.close(() => resolve()));
  await rm(dir, { recursive: true, force: true });
  vi.unstubAllEnvs();
  vi.doUnmock("node:module");
  vi.restoreAllMocks();
});

test("replies unsupported when the process has no compile cache enabled", async () => {
  void import("../src/node/entrypoint.mjs");

  expect(await warmReply()).toEqual({
    ok: false,
    state: "unsupported",
    entries: 0,
    loaded: 0,
    failures: [],
    stoppedBy: "complete",
    skipped: [],
    skippedCount: 0,
    bytes: 0,
    dir: null,
  });
});

test("reports the already-loaded app as warmed, sized off the flushed cache on disk", async () => {
  const cacheDir = join(dir, "compile-cache");
  await mkdir(cacheDir, { recursive: true });
  await writeFile(join(cacheDir, "one.blob"), Buffer.alloc(100));
  await writeFile(join(cacheDir, "two.blob"), Buffer.alloc(50));

  vi.doMock("node:module", async (orig) => {
    const actual = await orig<Record<string, unknown>>();
    return {
      ...actual,
      default: {
        ...(actual.default as Record<string, unknown>),
        getCompileCacheDir: () => cacheDir,
        flushCompileCache: () => {},
      },
    };
  });

  void import("../src/node/entrypoint.mjs");

  expect(await warmReply()).toEqual({
    ok: true,
    state: "warmed",
    entries: 1,
    loaded: 1,
    failures: [],
    stoppedBy: "complete",
    skipped: [],
    skippedCount: 0,
    bytes: 150,
    dir: cacheDir,
  });
});

test("replies unsupported rather than throwing when the flush itself throws", async () => {
  vi.doMock("node:module", async (orig) => {
    const actual = await orig<Record<string, unknown>>();
    return {
      ...actual,
      default: {
        ...(actual.default as Record<string, unknown>),
        getCompileCacheDir: () => join(dir, "compile-cache"),
        flushCompileCache: () => {
          throw new Error("boom");
        },
      },
    };
  });

  void import("../src/node/entrypoint.mjs");

  const reply = await warmReply();
  expect(reply.ok).toBe(false);
  expect(reply.state).toBe("unsupported");
});

test("replies unsupported when getCompileCacheDir is genuinely absent (old Node)", async () => {
  vi.doMock("node:module", async (orig) => {
    const actual = await orig<Record<string, unknown>>();
    const { getCompileCacheDir: _omit, ...rest } = actual.default as Record<string, unknown>;
    return { ...actual, default: { ...rest, flushCompileCache: () => {} } };
  });

  void import("../src/node/entrypoint.mjs");

  expect((await warmReply()).ok).toBe(false);
});
