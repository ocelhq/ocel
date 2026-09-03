import { describe, expect, it } from "vitest";
import { reconcile } from "./reconcile";
import { summaryTable } from "./summary";

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
});
