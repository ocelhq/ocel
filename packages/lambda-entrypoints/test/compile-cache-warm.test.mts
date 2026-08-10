import net from "node:net";
import { EventEmitter } from "node:events";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, expect, test, vi } from "vitest";

import { UNSUPPORTED_WARM as UNSUPPORTED } from "../src/shared/membrane.mjs";

type Msg = { type: string; payload: unknown };

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

const REPORT = {
  ok: true,
  state: "warmed",
  entries: 3,
  loaded: 3,
  failures: [],
  stoppedBy: "complete",
  skipped: [],
  skippedCount: 0,
  bytes: 4096,
  dir: "/tmp/v8-compile-cache",
};

async function warmOver(
  warm: unknown,
  request: { payload?: unknown } = {
    payload: { deadlineMs: 1_700_000_000_000, ceilingBytes: 64 << 20 },
  },
): Promise<Record<string, unknown>> {
  messages = [];
  bus = new EventEmitter();
  sockDir = await mkdtemp(join(tmpdir(), "ocel-warm-"));
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

  const { installCompileCacheWarm } = await import("../src/shared/membrane.mts");
  installCompileCacheWarm(warm);
  await waitFor(() => controlConns.size > 0);

  for (const conn of controlConns) {
    conn.write(JSON.stringify({ type: "warm-compile-cache", ...request }) + "\n");
  }
  await waitFor(() => messages.some((m) => m.type === "compile-cache-warmed"));

  return messages.find((m) => m.type === "compile-cache-warmed")!.payload as Record<
    string,
    unknown
  >;
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

test("replies with the launcher's report", async () => {
  expect(await warmOver(() => REPORT)).toEqual(REPORT);
});

test("hands the request's deadline and ceiling to the launcher", async () => {
  const seen: unknown[] = [];
  await warmOver(
    (options: unknown) => {
      seen.push(options);
      return REPORT;
    },
    { payload: { deadlineMs: 1_700_000_009_000, ceilingBytes: 1024 } },
  );

  expect(seen).toEqual([{ deadlineMs: 1_700_000_009_000, ceilingBytes: 1024 }]);
});

test("replies unsupported when the launcher exposes no warm function", async () => {
  expect(await warmOver(undefined)).toEqual(UNSUPPORTED);
});

test("replies unsupported rather than throwing when warming throws", async () => {
  expect(
    await warmOver(() => {
      throw new Error("boom");
    }),
  ).toEqual(UNSUPPORTED);
});

test("replies unsupported when warming returns no report", async () => {
  expect(await warmOver(() => undefined)).toEqual(UNSUPPORTED);
});

test("still warms when the request names neither bound", async () => {
  const seen: unknown[] = [];
  await warmOver((options: unknown) => {
    seen.push(options);
    return REPORT;
  }, {});

  expect(seen).toEqual([{ deadlineMs: undefined, ceilingBytes: undefined }]);
});
