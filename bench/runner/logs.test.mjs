import { describe, expect, it } from "vitest";

import { parseReportLine, parseReportLines, reportCountProblem, splitByInit } from "./logs.mjs";

const COLD =
  "REPORT RequestId: 8f7c2b1e-2a1f-4a55-9f0c-1d2e3f4a5b6c\tDuration: 1.23 ms\tBilled Duration: 2 ms\t" +
  "Memory Size: 1024 MB\tMax Memory Used: 90 MB\tInit Duration: 234.56 ms\t";

const WARM =
  "REPORT RequestId: 11111111-2222-3333-4444-555555555555\tDuration: 4.56 ms\tBilled Duration: 5 ms\t" +
  "Memory Size: 1024 MB\tMax Memory Used: 91 MB\t";

describe("parseReportLine", () => {
  it("reads every field off a cold REPORT line", () => {
    expect(parseReportLine(COLD)).toEqual({
      requestId: "8f7c2b1e-2a1f-4a55-9f0c-1d2e3f4a5b6c",
      durationMs: 1.23,
      billedMs: 2,
      memorySizeMb: 1024,
      maxMemoryUsedMb: 90,
      initDurationMs: 234.56,
    });
  });

  it("leaves initDurationMs null on a warm REPORT line", () => {
    expect(parseReportLine(WARM)).toMatchObject({ durationMs: 4.56, billedMs: 5, initDurationMs: null });
  });

  it("does not mistake Billed Duration or Init Duration for Duration", () => {
    const line = "REPORT RequestId: abc\tDuration: 999.99 ms\tBilled Duration: 1000 ms\tInit Duration: 7.00 ms";
    expect(parseReportLine(line)).toMatchObject({ durationMs: 999.99, billedMs: 1000, initDurationMs: 7 });
  });

  it("reads space-separated REPORT lines too", () => {
    const line =
      "REPORT RequestId: xyz Duration: 1.23 ms Billed Duration: 2 ms Memory Size: 1024 MB " +
      "Max Memory Used: 90 MB Init Duration: 234.56 ms";
    expect(parseReportLine(line)).toMatchObject({ requestId: "xyz", durationMs: 1.23, initDurationMs: 234.56 });
  });

  it("returns null for anything that is not a REPORT line", () => {
    expect(parseReportLine("START RequestId: abc Version: $LATEST")).toBe(null);
    expect(parseReportLine("END RequestId: abc")).toBe(null);
    expect(parseReportLine("2024-01-01T00:00:00.000Z\tabc\tINFO\tDuration: 1 ms")).toBe(null);
    expect(parseReportLine("")).toBe(null);
    expect(parseReportLine(null)).toBe(null);
  });

  it("survives a REPORT line missing the memory fields", () => {
    expect(parseReportLine("REPORT RequestId: abc\tDuration: 1.00 ms")).toEqual({
      requestId: "abc",
      durationMs: 1,
      billedMs: null,
      memorySizeMb: null,
      maxMemoryUsedMb: null,
      initDurationMs: null,
    });
  });
});

describe("parseReportLines", () => {
  it("keeps only the REPORT lines", () => {
    expect(parseReportLines([COLD, "START RequestId: abc", WARM, ""])).toHaveLength(2);
    expect(parseReportLines(undefined)).toEqual([]);
  });
});

describe("splitByInit", () => {
  it("calls a REPORT line with an Init Duration a cold invocation", () => {
    const { cold, warm } = splitByInit(parseReportLines([COLD, WARM, WARM]));
    expect(cold).toHaveLength(1);
    expect(warm).toHaveLength(2);
    expect(cold[0].initDurationMs).toBe(234.56);
  });
});

describe("reportCountProblem", () => {
  it("says nothing when every cold sample produced a cold REPORT line", () => {
    expect(reportCountProblem({ coldReports: 10, coldSamples: 10 })).toBe(null);
  });

  it("names both counts when they disagree", () => {
    const problem = reportCountProblem({ coldReports: 7, coldSamples: 10 });
    expect(problem).toContain("7 REPORT line(s)");
    expect(problem).toContain("10 cold sample(s)");
  });
});
