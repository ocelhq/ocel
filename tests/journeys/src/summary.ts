import { exitCodeFor, type Report, type ReportRow, type Verdict } from "./reconcile";

const MARKS: Record<Verdict, string> = {
  ok: "green",
  "expected-failure": "red (expected)",
  blocked: "blocked",
  "unexpected-failure": "NEW RED",
  "listed-and-passed": "FIXED, STILL LISTED",
  "never-ran": "NEVER RAN",
  disabled: "DISABLED",
  unplanned: "UNPLANNED",
};

function escape(value: string): string {
  return value.replace(/\|/g, "\\|").replace(/\n/g, " ");
}

function link(issue: string | undefined): string {
  if (!issue) {
    return "";
  }
  const number = issue.split("/").pop();
  return `[#${number}](${issue})`;
}

function row(entry: ReportRow): string {
  return `| ${escape(entry.cell)} | ${escape(entry.title)} | ${MARKS[entry.verdict]} | ${link(entry.issue)} |`;
}

export function summaryTable(
  report: Report,
  meta: { target: string; environment: string; runId: string },
): string {
  const counts = new Map<Verdict, number>();
  for (const entry of report.rows) {
    counts.set(entry.verdict, (counts.get(entry.verdict) ?? 0) + 1);
  }
  const tally = [...counts.entries()]
    .map(([verdict, count]) => `${MARKS[verdict]} ${count}`)
    .join(", ");

  return [
    `## journey · ${meta.target} · ${meta.environment} · run ${meta.runId}`,
    "",
    tally,
    "",
    "| cell | test | verdict | issue |",
    "| --- | --- | --- | --- |",
    ...report.rows.map(row),
    "",
  ].join("\n");
}

export function journeyVerdict(
  report: Report,
  unhandledErrors: string[],
): { exitCode: number; report: string } {
  const said = [
    failureReport(report),
    ...unhandledErrors.map((error) => `UNHANDLED — ${error.split("\n")[0]}`),
  ].filter((line) => line.length > 0);
  return {
    exitCode: unhandledErrors.length > 0 ? 1 : exitCodeFor(report.rows.map((row) => row.verdict)),
    report: said.join("\n"),
  };
}

export function failureReport(report: Report): string {
  return report.failures
    .map((entry) => {
      const because = entry.error ? `: ${entry.error.split("\n")[0]}` : "";
      return `${MARKS[entry.verdict]} — ${entry.cell} › ${entry.title}${because}`;
    })
    .join("\n");
}
