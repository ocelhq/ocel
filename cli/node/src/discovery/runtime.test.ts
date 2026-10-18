import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PROTOCOL_PREFIX } from "../protocol.js";
import { discover } from "./runtime.js";

type Events = "uncaughtException" | "unhandledRejection";

let listening: Record<Events, ReturnType<typeof process.listeners>>;
let exitCode: typeof process.exitCode;

function captureStdout(): string[] {
  const lines: string[] = [];
  vi.spyOn(process.stdout, "write").mockImplementation((chunk: unknown) => {
    lines.push(String(chunk));
    return true;
  });
  return lines;
}

function records(lines: string[]): unknown[] {
  return lines.map((line) => JSON.parse(line.slice(1 + PROTOCOL_PREFIX.length)));
}

beforeEach(() => {
  listening = {
    uncaughtException: process.listeners("uncaughtException"),
    unhandledRejection: process.listeners("unhandledRejection"),
  };
  exitCode = process.exitCode;
});

afterEach(() => {
  vi.restoreAllMocks();
  for (const event of ["uncaughtException", "unhandledRejection"] as const) {
    for (const listener of process.listeners(event)) {
      if (!listening[event].includes(listener)) process.removeListener(event, listener as never);
    }
  }
  process.exitCode = exitCode;
  globalThis.__ocelRegister = undefined;
});

describe("discover", () => {
  it("runs every import in order and ends the discovery span once the registrations settle", async () => {
    const emitted = captureStdout();
    const ran: string[] = [];
    let settled = false;
    await discover([
      async () => {
        ran.push("a");
        globalThis.__ocelRegister = [
          new Promise((resolve) => setTimeout(resolve, 10)).then(() => {
            settled = true;
          }),
        ];
      },
      async () => {
        ran.push("b");
      },
    ]);

    expect(ran).toEqual(["a", "b"]);
    expect(settled).toBe(true);
    expect(records(emitted)).toEqual([
      expect.objectContaining({ type: "span_start", stage: "discovery" }),
      expect.objectContaining({ type: "span_end", ok: true }),
    ]);
    expect(process.exitCode).toBe(exitCode);
  });

  it("ends the discovery span failed, names the error and fails the process when an import throws", async () => {
    const emitted = captureStdout();
    await discover([
      async () => {
        throw new Error("boom");
      },
    ]);

    expect(records(emitted)).toEqual([
      expect.objectContaining({ type: "span_start", stage: "discovery" }),
      expect.objectContaining({
        type: "error",
        stage: "discovery",
        message: expect.stringContaining("boom"),
      }),
      expect.objectContaining({ type: "span_end", ok: false }),
    ]);
    expect(process.exitCode).toBe(1);
  });
});
