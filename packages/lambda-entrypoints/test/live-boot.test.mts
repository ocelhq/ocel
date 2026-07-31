import net from "node:net";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { writeNextProjectFixture } from "./next-project-fixture.mjs";

// The real node entrypoint, booted against a real control socket. What this
// pins is the ordering the whole synchronous read surface rests on: the
// application's module scope — where a `const token = env.API_TOKEN` runs —
// must not run until the values it may read are in hand.
const LIVE_VALUES = Symbol.for("ocel.env.liveValues");

// The app records what it saw at import, which is the moment a module-scope
// read happens. Reading the published values the way the SDK does keeps the
// assertion about delivery rather than about a marker file.
const appModule = `
globalThis.__appImportedAt = (globalThis.__appImportedAt ?? 0) + 1;
globalThis.__appSawLive = globalThis[Symbol.for("ocel.env.liveValues")]?.values?.API_TOKEN;
export default (req, res) => res.end("ok");
`;

let dir: string;
let server: net.Server;
let connections: net.Socket[];
let messages: { type: string; payload: any }[];

function push(message: unknown): void {
  for (const conn of connections) conn.write(JSON.stringify(message) + "\n");
}

function waitForConnection(): Promise<void> {
  return new Promise((resolve) => {
    if (connections.length > 0) return resolve();
    server.once("connection", () => resolve());
  });
}

async function waitFor(pred: () => boolean, label: string): Promise<void> {
  for (let i = 0; i < 200; i++) {
    if (pred()) return;
    await new Promise((r) => setTimeout(r, 10));
  }
  throw new Error(`timed out waiting for ${label}`);
}

async function settle(): Promise<void> {
  for (let i = 0; i < 20; i++) await new Promise((r) => setTimeout(r, 5));
}

beforeEach(async () => {
  delete (globalThis as Record<symbol, unknown>)[LIVE_VALUES];
  delete (globalThis as any).__appImportedAt;
  delete (globalThis as any).__appSawLive;
  connections = [];
  messages = [];
  dir = await mkdtemp(join(tmpdir(), "ocel-live-boot-"));

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
  await writeFile(handler, appModule);
  vi.stubEnv("OCEL_CONTROL_SOCKET", join(dir, "control.sock"));
  vi.stubEnv("OCEL_HANDLER", handler);
  vi.resetModules();
});

afterEach(async () => {
  for (const conn of connections) conn.destroy();
  await new Promise<void>((resolve) => server.close(() => resolve()));
  await rm(dir, { recursive: true, force: true });
  vi.unstubAllEnvs();
});

test("holds the application's import until the first push, then runs it with the values in hand", async () => {
  vi.stubEnv("OCEL_LIVE_KEYS", "API_TOKEN");

  void import("../src/node/entrypoint.mjs");
  await waitForConnection();
  await settle();

  // The membrane's fetch is still outstanding; nothing of the app has run.
  expect((globalThis as any).__appImportedAt).toBeUndefined();
  expect(messages.some((m) => m.type === "server-ready")).toBe(false);

  push({ type: "liveValues", generation: 1, values: { API_TOKEN: "sk_live_boot" } });

  await waitFor(() => messages.some((m) => m.type === "server-ready"), "server-ready");
  expect((globalThis as any).__appImportedAt).toBe(1);
  expect((globalThis as any).__appSawLive).toBe("sk_live_boot");
});

// The Next entrypoint loads the app the same way and owes the same ordering:
// a Next app's module scope is where a `const token = env.API_TOKEN` lives too.
test("holds the Next launcher's import until the first push", async () => {
  vi.stubEnv("OCEL_LIVE_KEYS", "API_TOKEN");
  const projectDir = join(dir, "project");
  await writeNextProjectFixture(projectDir);
  const launcher = join(projectDir, "__next_launcher.cjs");
  await writeFile(
    launcher,
    `globalThis.__appImportedAt = (globalThis.__appImportedAt ?? 0) + 1;
globalThis.__appSawLive = globalThis[Symbol.for("ocel.env.liveValues")]?.values?.API_TOKEN;
module.exports = { handler(req, res) { res.end("ok"); } };
`,
  );
  vi.stubEnv("OCEL_HANDLER", launcher);

  void import("../src/next/entrypoint.mjs");
  await waitForConnection();
  await settle();
  expect((globalThis as any).__appImportedAt).toBeUndefined();

  push({ type: "liveValues", generation: 1, values: { API_TOKEN: "sk_live_next" } });

  await waitFor(() => messages.some((m) => m.type === "server-ready"), "server-ready");
  expect((globalThis as any).__appSawLive).toBe("sk_live_next");
});

test("runs the application straight away when the function declares nothing live", async () => {
  void import("../src/node/entrypoint.mjs");

  await waitFor(() => messages.some((m) => m.type === "server-ready"), "server-ready");
  expect((globalThis as any).__appImportedAt).toBe(1);
  expect((globalThis as any).__appSawLive).toBeUndefined();
});
