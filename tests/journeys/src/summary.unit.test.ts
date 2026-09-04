import { describe, expect, it } from "vitest";
import { reconcile } from "./reconcile";
import { journeyVerdict, summaryTable } from "./summary";

const ISSUE = "https://github.com/ocelhq/ocel/issues/851";

describe("summary table", () => {
  const report = reconcile({
    planned: [
      { cell: "express/web", title: "up", leg: "up" },
      { cell: "express/web", title: "GET /health | answers", leg: "contract" },
    ],
    results: [
      { cell: "express/web", title: "up", outcome: "passed" },
      { cell: "express/web", title: "GET /health | answers", outcome: "failed" },
    ],
    expectations: { "express/web": { "GET /health | answers": ISSUE } },
  });
  const table = summaryTable(report, {
    target: "dev",
    environment: "dev",
    runId: "local-ada",
    leftOut: [],
  });

  it("heads the table with the target, environment and run", () => {
    expect(table.split("\n")[0]).toBe("## journey · dev · dev · run local-ada");
  });

  it("carries one markdown row per planned test", () => {
    const rows = table.split("\n").filter((line) => line.startsWith("| express/web"));
    expect(rows).toHaveLength(2);
  });

  it("escapes a pipe inside a test title", () => {
    expect(table).toContain("GET /health \\| answers");
  });

  it("links the issue that owns a red cell", () => {
    expect(table).toContain(`[#851](${ISSUE})`);
  });

  it("says nothing about a pick when the pass ran everything", () => {
    expect(table).not.toContain("left out this pass");
  });

  it("names what the pass left out, above the tally", () => {
    const said = summaryTable(report, {
      target: "dev",
      environment: "dev",
      runId: "local-ada",
      leftOut: ["hono", "fastify"],
    });
    const lines = said.split("\n");
    expect(lines[2]).toBe("left out this pass: hono, fastify");
    expect(lines.indexOf("left out this pass: hono, fastify")).toBeLessThan(
      lines.findIndex((line) => line.includes("green")),
    );
  });
});

describe("journey verdict", () => {
  const green = reconcile({
    planned: [{ cell: "express/web", title: "up", leg: "up" }],
    results: [{ cell: "express/web", title: "up", outcome: "passed" }],
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
      planned: [{ cell: "express/web", title: "up", leg: "up" }],
      results: [{ cell: "express/web", title: "up", outcome: "failed" }],
      expectations: {},
    });
    const verdict = journeyVerdict(red, ["Error: unhandled"]);
    expect(verdict.exitCode).toBe(1);
    expect(verdict.report.split("\n")).toHaveLength(2);
  });
});
