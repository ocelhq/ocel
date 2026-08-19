import net from "node:net";
import { EventEmitter } from "node:events";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, expect, test } from "vitest";

import { UNSUPPORTED_WARM } from "../src/shared/membrane.mjs";

const appModule = `export default (req, res) => res.end("ok");
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
  dir = await mkdtemp(join(tmpdir(), "ocel-node-entry-"));

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

  const handler = join(dir, "app.mjs");
  await writeFile(handler, appModule);

  process.env.OCEL_CONTROL_SOCKET = sockPath;
  process.env.OCEL_HANDLER = handler;
  await import("../src/node/entrypoint.mjs");
  await waitFor(() => messages.some((m) => m.type === "server-ready"));
  port = messages.find((m) => m.type === "server-ready")!.payload.httpPort;
});

afterAll(async () => {
  for (const c of controlConns) c.end();
  await new Promise<void>((resolve) => controlServer.close(() => resolve()));
  await rm(dir, { recursive: true, force: true });
  delete process.env.OCEL_CONTROL_SOCKET;
  delete process.env.OCEL_HANDLER;
});

test("the loaded application answers over the loopback server", async () => {
  const response = await fetch(`http://127.0.0.1:${port}/anything`);

  expect(response.status).toBe(200);
  expect(await response.text()).toBe("ok");
});

test("a warm request is answered unsupported rather than left to time out", async () => {
  for (const conn of controlConns) {
    conn.write(
      JSON.stringify({
        type: "warm-compile-cache",
        payload: { deadlineMs: 1_700_000_000_000, ceilingBytes: 64 << 20 },
      }) + "\n",
    );
  }
  await waitFor(() => messages.some((m) => m.type === "compile-cache-warmed"));

  expect(messages.find((m) => m.type === "compile-cache-warmed")!.payload).toEqual(
    UNSUPPORTED_WARM,
  );
});
