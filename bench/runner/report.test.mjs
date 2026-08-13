import { describe, expect, it } from "vitest";

import { PINNED, SAMPLES } from "../matrix.config.mjs";
import { cellRow, renderTable, resultsPayload, summarizeCell } from "./report.mjs";

function measuredCell(overrides = {}) {
  return {
    id: "express/ocel-bundle",
    app: "express",
    platform: "ocel-bundle",
    status: "measured",
    error: null,
    deploy: { wallMs: 12_000, buildMs: 4_000, provisionMs: 8_000 },
    measurement: {
      cold: {
        samples: [{ rttMs: 400 }, { rttMs: 500 }, { rttMs: 600 }],
        reports: [{ initDurationMs: 200 }, { initDurationMs: 220 }, { initDurationMs: 240 }],
      },
      warm: {
        samples: [{ rttMs: 20 }, { rttMs: 30 }, { rttMs: 40 }],
        reports: [{ durationMs: 1 }, { durationMs: 2 }, { durationMs: 3 }],
      },
      errors: [],
      warnings: [],
    },
    ...overrides,
  };
}

describe("summarizeCell", () => {
  it("summarizes client RTT and the lambda REPORT numbers apart", () => {
    const summary = summarizeCell(measuredCell());
    expect(summary.rttCold.p50).toBe(500);
    expect(summary.rttWarm.p50).toBe(30);
    expect(summary.initDuration.p50).toBe(220);
    expect(summary.lambdaDuration.p50).toBe(2);
  });

  it("summarizes a cell that never measured anything without blowing up", () => {
    expect(summarizeCell({ status: "failed", measurement: null }).rttCold.n).toBe(0);
  });
});

describe("cellRow", () => {
  it("renders seconds for deploys and milliseconds for latencies", () => {
    expect(cellRow(measuredCell())).toMatchObject({
      platform: "ocel-bundle",
      deploy: "12.0",
      build: "4.0",
      provision: "8.0",
      coldP50: "500.0",
      coldP99: "600.0",
      initP50: "220.0",
      warmP50: "30.0",
      durP50: "2.0",
      note: "",
    });
  });

  it("dashes out every number of a failed cell and marks it", () => {
    const row = cellRow({ id: "hono/sst", app: "hono", platform: "sst", status: "failed", error: "boom\ndetail" });
    expect(row.coldP50).toBe("-");
    expect(row.deploy).toBe("-");
    expect(row.note).toBe("FAILED boom");
  });

  it("marks a skipped cell as skipped, never as passed", () => {
    const row = cellRow({ platform: "raw", status: "skipped", error: "no aws credentials" });
    expect(row.note).toBe("SKIPPED no aws credentials");
  });

  it("marks a cell that was cut short, so its p99 is never read as a full run", () => {
    expect(cellRow(measuredCell(), SAMPLES).note).toBe(
      `partial: 3/${SAMPLES.cold} cold, 3/${SAMPLES.warm} warm`,
    );
  });

  it("counts bad samples and warnings", () => {
    const cell = measuredCell();
    cell.measurement.errors = ["cold sample 1: 502"];
    cell.measurement.warnings = ["cold REPORT mismatch"];
    expect(cellRow(cell).note).toBe("1 bad sample(s), 1 warning(s)");
  });
});

describe("renderTable", () => {
  const table = renderTable({
    cells: [measuredCell(), { ...measuredCell(), id: "hono/sst", app: "hono", platform: "sst" }],
    pinned: PINNED,
    region: "us-east-1",
    samples: SAMPLES,
  });

  it("prints the pinned config so no number is read detached from it", () => {
    expect(table).toContain(PINNED.runtime);
    expect(table).toContain(`${PINNED.memoryMB} MB`);
    expect(table).toContain(PINNED.architecture);
    expect(table).toContain("us-east-1");
    expect(table).toContain(`${SAMPLES.cold} cold sample(s)`);
    expect(table).toContain(`${SAMPLES.warm} warm sample(s)`);
  });

  it("groups rows by framework", () => {
    expect(table).toContain("\nexpress\n");
    expect(table).toContain("\nhono\n");
  });

  it("right-aligns each number under its column label", () => {
    const lines = table.split("\n");
    const header = lines.find((line) => line.includes("platform") && line.includes("deploy s"));
    const row = lines.find((line) => line.includes("ocel-bundle"));
    expect(header.indexOf("deploy s") + "deploy s".length).toBe(row.indexOf("12.0") + "12.0".length);
    expect(header.indexOf("init p50") + "init p50".length).toBe(row.indexOf("220.0") + "220.0".length);
  });

  it("surfaces warnings under the table", () => {
    const cell = measuredCell();
    cell.measurement.warnings = ["7 REPORT line(s) carry an Init Duration but 10 cold sample(s) were driven"];
    const withWarning = renderTable({ cells: [cell], pinned: PINNED, region: "us-east-1", samples: SAMPLES });
    expect(withWarning).toContain("warnings");
    expect(withWarning).toContain("express/ocel-bundle: 7 REPORT line(s)");
  });
});

describe("resultsPayload", () => {
  it("carries every raw sample, not just the summaries", () => {
    const payload = resultsPayload({
      cells: [measuredCell()],
      pinned: PINNED,
      region: "us-east-1",
      samples: SAMPLES,
      startedAt: 0,
      finishedAt: 1000,
      aborted: false,
    });
    expect(payload.cells[0].measurement.warm.samples).toHaveLength(3);
    expect(payload.cells[0].summary.rttWarm.p50).toBe(30);
    expect(payload.startedAt).toBe("1970-01-01T00:00:00.000Z");
    expect(payload.percentileMethod).toContain("nearest-rank");
  });
});
