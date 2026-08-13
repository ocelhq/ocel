import { afterEach, describe, expect, it, vi } from "vitest";
import { PROTOCOL_PREFIX, isReported, log, reportError, withSpan } from "./protocol.js";

function captureStdout(): { lines: string[] } {
  const lines: string[] = [];
  vi.spyOn(process.stdout, "write").mockImplementation((chunk: unknown) => {
    lines.push(String(chunk));
    return true;
  });
  return { lines };
}

function parseRecord(line: string): unknown {
  expect(line.startsWith("\n" + PROTOCOL_PREFIX)).toBe(true);
  return JSON.parse(line.slice(1 + PROTOCOL_PREFIX.length));
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("protocol records", () => {
  it("prefixes every record with the wire-format discriminator", () => {
    const { lines } = captureStdout();
    log("info", "installing dependencies", "api", "build");

    expect(lines).toHaveLength(1);
    expect(parseRecord(lines[0]!)).toEqual({
      type: "log",
      level: "info",
      message: "installing dependencies",
      app: "api",
      stage: "build",
    });
  });

  it("leads every record with its own newline, so a framework's unterminated partial line cannot glue onto it", () => {
    const { lines } = captureStdout();
    reportError("no entrypoint resolved");

    expect(lines[0]).toMatch(/^\n@@OCEL_V1@@/);
  });

  it("reports a span_start/span_end pair around a successful call", async () => {
    const { lines } = captureStdout();
    const result = await withSpan("build", "api", async () => "done");

    expect(result).toBe("done");
    const records = lines.map(parseRecord);
    expect(records).toEqual([
      { type: "span_start", id: expect.any(String), stage: "build", app: "api" },
      { type: "span_end", id: expect.any(String), ok: true },
    ]);
    expect((records[0] as { id: string }).id).toBe((records[1] as { id: string }).id);
  });

  it("reports the actual error and a failed span_end, then rethrows the same error", async () => {
    const { lines } = captureStdout();
    const failure = new Error("no entrypoint resolved");

    await expect(withSpan("build", "api", async () => { throw failure; })).rejects.toBe(failure);

    const records = lines.map(parseRecord);
    expect(records[0]).toMatchObject({ type: "span_start", stage: "build", app: "api" });
    expect(records[1]).toMatchObject({ type: "error", app: "api", stage: "build" });
    expect((records[1] as { message: string }).message).toContain("no entrypoint resolved");
    expect(records[2]).toMatchObject({ type: "span_end", ok: false });
  });

  it("marks an error reported by withSpan so an outer catch does not report it again", async () => {
    captureStdout();
    const failure = new Error("boom");

    expect(isReported(failure)).toBe(false);
    await expect(withSpan("build", "api", async () => { throw failure; })).rejects.toBe(failure);
    expect(isReported(failure)).toBe(true);
  });

  it("does not mistake an ordinary error for one already reported", () => {
    expect(isReported(new Error("unrelated"))).toBe(false);
    expect(isReported("a plain string throw")).toBe(false);
  });

  it("carries no app/stage when the failure is not scoped to either", () => {
    const { lines } = captureStdout();
    reportError("could not detect a framework");

    expect(parseRecord(lines[0]!)).toEqual({ type: "error", message: "could not detect a framework" });
  });
});
