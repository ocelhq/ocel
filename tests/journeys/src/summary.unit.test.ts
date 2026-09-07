import { describe, expect, it } from "bun:test";
import { reconcile } from "./reconcile";
import { journeyVerdict, summaryTable } from "./summary";

const GAP = { id: "cloudfront-stub", reason: "the edge is not backed", issue: 852 };

describe("summary table", () => {
  const report = reconcile({
    planned: [
      { cell: "node/web", title: "up", leg: "up" },
      { cell: "node/web", title: "GET /health | answers", leg: "contract" },
    ],
    results: [
      { cell: "node/web", title: "up", outcome: "passed" },
      { cell: "node/web", title: "GET /health | answers", outcome: "failed" },
    ],
    expectations: { "node/web": { "GET /health | answers": [GAP] } },
  });
  const table = summaryTable(report, {
    target: "dev",
    environment: "dev",
    runId: "local-ada",
  });

  it("heads the table with the target, environment and run", () => {
    expect(table.split("\n")[0]).toBe("## journey · dev · dev · run local-ada");
  });

  it("lists only the tests that were not green", () => {
    const rows = table.split("\n").filter((line) => line.startsWith("| node/web"));
    expect(rows).toHaveLength(1);
    expect(rows[0]).toContain("GET /health");
  });

  it("still counts the green tests in the tally", () => {
    expect(table.split("\n")[2]).toBe("green 1, red (expected) 1");
  });

  it("drops the table when everything was green", () => {
    const green = reconcile({
      planned: [{ cell: "node/web", title: "up", leg: "up" }],
      results: [{ cell: "node/web", title: "up", outcome: "passed" }],
      expectations: {},
    });
    const said = summaryTable(green, {
      target: "dev",
      environment: "dev",
      runId: "local-ada",
    });
    expect(said).not.toContain("| cell |");
    expect(said).toContain("green 1");
  });

  it("escapes a pipe inside a test title", () => {
    expect(table).toContain("GET /health \\| answers");
  });

  it("links the issue that owns a red cell", () => {
    expect(table).toContain("cloudfront-stub [#852](https://github.com/ocelhq/ocel/issues/852)");
  });
});

describe("journey verdict", () => {
  const green = reconcile({
    planned: [{ cell: "node/web", title: "up", leg: "up" }],
    results: [{ cell: "node/web", title: "up", outcome: "passed" }],
    expectations: {},
  });

  it("is zero when the account reconciles and nothing was thrown outside a test", () => {
    expect(journeyVerdict(green, [])).toEqual({ exitCode: 0, report: "" });
  });

  it("is one when a test threw outside the run, and names what threw", () => {
    const verdict = journeyVerdict(green, ["Error: the pool was closed\n  at pg.end"]);
    expect(verdict.exitCode).toBe(1);
    expect(verdict.report).toBe("UNHANDLED — Error: the pool was closed");
  });

  it("names the unreconciled rows alongside what threw", () => {
    const red = reconcile({
      planned: [{ cell: "node/web", title: "up", leg: "up" }],
      results: [{ cell: "node/web", title: "up", outcome: "failed" }],
      expectations: {},
    });
    const verdict = journeyVerdict(red, ["Error: unhandled"]);
    expect(verdict.exitCode).toBe(1);
    expect(verdict.report.split("\n")).toHaveLength(2);
  });
});
