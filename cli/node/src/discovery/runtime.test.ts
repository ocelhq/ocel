import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PROTOCOL_PREFIX } from "../protocol.js";
import { attributes, named, tracedRun, useCollector } from "../tracing/test-collector.js";
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
      {
        file: "declarations/a.ts",
        load: async () => {
          ran.push("a");
          globalThis.__ocelRegister = [
            new Promise((resolve) => setTimeout(resolve, 10)).then(() => {
              settled = true;
            }),
          ];
        },
      },
      {
        file: "declarations/b.ts",
        load: async () => {
          ran.push("b");
        },
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
      {
        file: "declarations/boom.ts",
        load: async () => {
          throw new Error("boom");
        },
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

useCollector();

describe("discovery under a run's trace", () => {
  it("names the project file of each module it imports on that module's import span", async () => {
    const { spans } = await tracedRun(`
      import { discover } from "@src/discovery/runtime.ts";
      await discover([
        { file: "declarations/queues.ts", load: async () => {} },
        { file: "declarations/buckets.ts", load: async () => {} },
      ]);
    `);

    const imports = spans
      .filter((s) => s.name === "import module")
      .map((s) => attributes(s)["code.filepath"]);
    expect(imports).toEqual(["declarations/queues.ts", "declarations/buckets.ts"]);
    expect(attributes(named(spans, "discover resources"))["ocel.import_count"]).toBe(2);
    expect(attributes(named(spans, "register resources"))["ocel.registration_count"]).toBe(0);
  });
});
