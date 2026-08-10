import net from "node:net";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

const LIVE_VALUES = Symbol.for("ocel.env.liveValues");

type LiveState = { generation: number; values: Record<string, string> } | undefined;

let sockDir: string;
let server: net.Server;
let connections: net.Socket[];

function published(): LiveState {
  return (globalThis as Record<symbol, unknown>)[LIVE_VALUES] as LiveState;
}

function push(message: unknown): void {
  for (const conn of connections) conn.write(JSON.stringify(message) + "\n");
}

function waitForConnection(): Promise<void> {
  return new Promise((resolve) => {
    if (connections.length > 0) return resolve();
    server.once("connection", () => resolve());
  });
}

async function settle(): Promise<void> {
  for (let i = 0; i < 10; i++) await new Promise((r) => setImmediate(r));
}

async function load() {
  vi.resetModules();
  return import("../src/shared/live-values.mts");
}

beforeEach(async () => {
  delete (globalThis as Record<symbol, unknown>)[LIVE_VALUES];
  connections = [];
  sockDir = await mkdtemp(join(tmpdir(), "ocel-live-"));
  const sockPath = join(sockDir, "control.sock");
  server = net.createServer((conn) => connections.push(conn));
  await new Promise<void>((resolve) => server.listen(sockPath, resolve));
  vi.stubEnv("OCEL_CONTROL_SOCKET", sockPath);
});

afterEach(async () => {
  for (const conn of connections) conn.destroy();
  await new Promise<void>((resolve) => server.close(() => resolve()));
  await rm(sockDir, { recursive: true, force: true });
  vi.unstubAllEnvs();
});

describe("receiving a push", () => {
  test("publishes the values it carried under the generation it named", async () => {
    vi.stubEnv("OCEL_LIVE_KEYS", "API_TOKEN");
    const { awaitLiveValues } = await load();

    const ready = awaitLiveValues();
    await waitForConnection();
    push({ type: "liveValues", generation: 1, values: { API_TOKEN: "sk_live_1" } });
    await ready;

    expect(published()).toEqual({ generation: 1, values: { API_TOKEN: "sk_live_1" } });
  });

  test("replaces the values on a later generation", async () => {
    vi.stubEnv("OCEL_LIVE_KEYS", "API_TOKEN");
    const { awaitLiveValues } = await load();

    const ready = awaitLiveValues();
    await waitForConnection();
    push({ type: "liveValues", generation: 1, values: { API_TOKEN: "first" } });
    await ready;
    push({ type: "liveValues", generation: 4, values: { API_TOKEN: "fourth" } });
    await settle();

    expect(published()).toEqual({ generation: 4, values: { API_TOKEN: "fourth" } });
  });

  test.each([
    ["an older generation", 1],
    ["the generation already applied", 2],
  ])("ignores %s", async (_name, generation) => {
    vi.stubEnv("OCEL_LIVE_KEYS", "API_TOKEN");
    const { awaitLiveValues } = await load();

    const ready = awaitLiveValues();
    await waitForConnection();
    push({ type: "liveValues", generation: 2, values: { API_TOKEN: "current" } });
    await ready;
    push({ type: "liveValues", generation, values: { API_TOKEN: "stale" } });
    await settle();

    expect(published()).toEqual({ generation: 2, values: { API_TOKEN: "current" } });
  });

  test.each([
    ["a message of another type", { type: "log", generation: 1, values: { A: "x" } }],
    ["no generation", { type: "liveValues", values: { A: "x" } }],
    ["a generation below one", { type: "liveValues", generation: 0, values: { A: "x" } }],
    [
      "a fractional generation",
      { type: "liveValues", generation: 1.5, values: { A: "x" } },
    ],
    ["no values", { type: "liveValues", generation: 1 }],
    [
      "a value that is not a string",
      { type: "liveValues", generation: 1, values: { A: 7 } },
    ],
  ])("ignores %s", async (_name, message) => {
    vi.stubEnv("OCEL_LIVE_KEYS", "A");
    const { awaitLiveValues } = await load();

    void awaitLiveValues();
    await waitForConnection();
    push(message);
    await settle();

    expect(published()).toBeUndefined();
  });

  test("ignores a line that is not JSON at all, and keeps reading after it", async () => {
    vi.stubEnv("OCEL_LIVE_KEYS", "A");
    const { awaitLiveValues } = await load();

    const ready = awaitLiveValues();
    await waitForConnection();
    connections[0]!.write("{ not json\n");
    push({ type: "liveValues", generation: 1, values: { A: "x" } });
    await ready;

    expect(published()).toEqual({ generation: 1, values: { A: "x" } });
  });

  test("reassembles a message split across two writes", async () => {
    vi.stubEnv("OCEL_LIVE_KEYS", "A");
    const { awaitLiveValues } = await load();

    const ready = awaitLiveValues();
    await waitForConnection();
    const line = JSON.stringify({ type: "liveValues", generation: 1, values: { A: "x" } });
    connections[0]!.write(line.slice(0, 20));
    await settle();
    connections[0]!.write(line.slice(20) + "\n");
    await ready;

    expect(published()).toEqual({ generation: 1, values: { A: "x" } });
  });
});

describe("a function that declares no live value", () => {
  test.each([
    ["the variable is absent", undefined],
    ["the variable is empty", ""],
  ])("does not wait, and opens no control connection, when %s", async (_name, value) => {
    if (value === undefined) vi.stubEnv("OCEL_LIVE_KEYS", undefined as never);
    else vi.stubEnv("OCEL_LIVE_KEYS", value);
    const { awaitLiveValues } = await load();

    await awaitLiveValues();
    await settle();

    expect(connections).toHaveLength(0);
    expect(published()).toBeUndefined();
  });
});

describe("a function that declares live values", () => {
  test("does not resolve before the first push arrives", async () => {
    vi.stubEnv("OCEL_LIVE_KEYS", "API_TOKEN");
    const { awaitLiveValues } = await load();

    let resolved = false;
    void awaitLiveValues().then(() => (resolved = true));
    await waitForConnection();
    await settle();
    expect(resolved).toBe(false);

    push({ type: "liveValues", generation: 1, values: { API_TOKEN: "sk" } });
    await settle();
    expect(resolved).toBe(true);
  });

  test("waits once: a second caller after the push is already satisfied", async () => {
    vi.stubEnv("OCEL_LIVE_KEYS", "API_TOKEN");
    const { awaitLiveValues } = await load();

    const first = awaitLiveValues();
    await waitForConnection();
    push({ type: "liveValues", generation: 1, values: { API_TOKEN: "sk" } });
    await first;

    await expect(awaitLiveValues()).resolves.toBeUndefined();
    expect(connections).toHaveLength(1);
  });
});
