import { issueUrl, type Listed, type Skipped } from "./expectations";
import { exitCodeFor, type Report, type ReportRow, type Verdict } from "./reconcile";

export type SummaryMeta = {
  target: string;
  environment: string;
  runId: string;
  leftOut: string[];
  skipped?: Skipped;
};

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

function link(gap: Listed): string {
  return gap.issue === undefined ? gap.id : `${gap.id} [#${gap.issue}](${issueUrl(gap.issue)})`;
}

function row(entry: ReportRow): string {
  const why = entry.listed.map(link).join(", ");
  return `| ${escape(entry.cell)} | ${escape(entry.title)} | ${MARKS[entry.verdict]} | ${why} |`;
}

function gapLines(report: Report): string[] {
  const counts = new Map<string, { gap: Listed; red: number }>();
  for (const entry of report.rows) {
    for (const gap of entry.listed) {
      const tally = counts.get(gap.id) ?? { gap, red: 0 };
      if (entry.verdict === "expected-failure") {
        tally.red += 1;
      }
      counts.set(gap.id, tally);
    }
  }
  return [...counts.values()].map(
    ({ gap, red }) => `- ${link(gap)} — ${escape(gap.reason)} — red (expected) ${red}`,
  );
}

function skippedLines(skipped: Skipped): string[] {
  return Object.entries(skipped).map(
    ([cell, listed]) => `- skipped ${escape(cell)} — ${listed.map(link).join(", ")}`,
  );
}

function redTable(report: Report): string[] {
  const red = report.rows.filter((entry) => entry.verdict !== "ok");
  if (red.length === 0) {
    return [];
  }
  return ["| cell | test | verdict | gap |", "| --- | --- | --- | --- |", ...red.map(row), ""];
}

export function summaryTable(report: Report, meta: SummaryMeta): string {
  const counts = new Map<Verdict, number>();
  for (const entry of report.rows) {
    counts.set(entry.verdict, (counts.get(entry.verdict) ?? 0) + 1);
  }
  const skipped = Object.keys(meta.skipped ?? {}).length;
  const tally = [
    ...[...counts.entries()].map(([verdict, count]) => `${MARKS[verdict]} ${count}`),
    ...(skipped > 0 ? [`skipped cells ${skipped}`] : []),
  ].join(", ");

  return [
    `## journey · ${meta.target} · ${meta.environment} · run ${meta.runId}`,
    "",
    ...(meta.leftOut.length > 0 ? [`left out this pass: ${meta.leftOut.join(", ")}`, ""] : []),
    tally,
    "",
    ...gapLines(report),
    ...skippedLines(meta.skipped ?? {}),
    "",
    ...redTable(report),
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
