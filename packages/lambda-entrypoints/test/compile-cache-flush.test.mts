import net from "node:net";
import { EventEmitter } from "node:events";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, expect, test, vi } from "vitest";

type Msg = { type: string; payload: unknown };
type FlushReply = { dir: string | null; ok: boolean };

let sockDir: string;
let controlServer: net.Server;
const controlConns = new Set<net.Socket>();
let messages: Msg[];
let bus: EventEmitter;

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

// Stubs the default export of node:module before importing membrane.mts fresh
// (the source reads Module.flushCompileCache/getCompileCacheDir off it), then
// boots a real control socket, sends flush-compile-cache down it, and returns
// the real compile-cache-flushed reply. `build` gets the real default export
// and returns what stands in for it — deleting a key (rather than setting it
// to undefined) is what makes a case a genuinely absent API, not just an
// undefined-valued one.
async function flushOver(
  build: (actualDefault: Record<string, unknown>) => Record<string, unknown>,
): Promise<FlushReply> {
  vi.doMock("node:module", async (orig) => {
    const actual = await orig<Record<string, unknown>>();
    return { ...actual, default: build(actual.default as Record<string, unknown>) };
  });

  messages = [];
  bus = new EventEmitter();
  sockDir = await mkdtemp(join(tmpdir(), "ocel-flush-"));
  const sockPath = join(sockDir, "control.sock");

  controlServer = net.createServer((conn) => {
    controlConns.add(conn);
    bus.emit("msg");
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

  const { installCompileCacheFlush } = await import("../src/shared/membrane.mts");
  installCompileCacheFlush();
  await waitFor(() => controlConns.size > 0);

  for (const conn of controlConns) {
    conn.write(JSON.stringify({ type: "flush-compile-cache" }) + "\n");
  }
  await waitFor(() => messages.some((m) => m.type === "compile-cache-flushed"));

  return messages.find((m) => m.type === "compile-cache-flushed")!.payload as FlushReply;
}

afterEach(async () => {
  for (const c of controlConns) c.destroy();
  controlConns.clear();
  await new Promise<void>((resolve) => controlServer.close(() => resolve()));
  await rm(sockDir, { recursive: true, force: true });
  delete process.env.OCEL_CONTROL_SOCKET;
  vi.resetModules();
  vi.restoreAllMocks();
});

test("replies ok:true with the cache dir when both functions exist and flush succeeds", async () => {
  const payload = await flushOver((actual) => ({
    ...actual,
    getCompileCacheDir: () => "/tmp/v8-compile-cache",
    flushCompileCache: () => {},
  }));

  expect(payload).toEqual({ dir: "/tmp/v8-compile-cache", ok: true });
});

test("replies ok:false when flushCompileCache is genuinely absent (old Node)", async () => {
  const payload = await flushOver((actual) => {
    const { flushCompileCache: _omit, ...rest } = actual;
    return { ...rest, getCompileCacheDir: () => "/tmp/v8-compile-cache" };
  });

  expect(payload).toEqual({ dir: "/tmp/v8-compile-cache", ok: false });
});

test("replies ok:false with dir:null when getCompileCacheDir is genuinely absent (old Node)", async () => {
  const payload = await flushOver((actual) => {
    const { getCompileCacheDir: _omit, ...rest } = actual;
    return { ...rest, flushCompileCache: () => {} };
  });

  expect(payload).toEqual({ dir: null, ok: false });
});

test("replies ok:false without throwing when flushCompileCache throws", async () => {
  const payload = await flushOver((actual) => ({
    ...actual,
    getCompileCacheDir: () => "/tmp/v8-compile-cache",
    flushCompileCache: () => {
      throw new Error("boom");
    },
  }));

  expect(payload).toEqual({ dir: "/tmp/v8-compile-cache", ok: false });
});
