import { aws, logGroupFor } from "./aws.mjs";

export const LOG_PAGE_LIMIT = 1000;

export const REPORT_FILTER_PATTERN = "REPORT";

const REQUEST_ID = /^REPORT\s+RequestId:\s*(\S+)/;
const DURATION = /(?<!Billed |Init )Duration:\s*([\d.]+)\s*ms/;
const BILLED = /Billed Duration:\s*([\d.]+)\s*ms/;
const MEMORY_SIZE = /Memory Size:\s*([\d.]+)\s*MB/;
const MAX_MEMORY = /Max Memory Used:\s*([\d.]+)\s*MB/;
const INIT_DURATION = /Init Duration:\s*([\d.]+)\s*ms/;

export function parseReportLine(line) {
  const text = String(line ?? "").trim();
  const id = REQUEST_ID.exec(text);
  if (!id) return null;
  return {
    requestId: id[1],
    durationMs: number(DURATION.exec(text)),
    billedMs: number(BILLED.exec(text)),
    memorySizeMb: number(MEMORY_SIZE.exec(text)),
    maxMemoryUsedMb: number(MAX_MEMORY.exec(text)),
    initDurationMs: number(INIT_DURATION.exec(text)),
  };
}

function number(match) {
  return match ? Number(match[1]) : null;
}

export function parseReportLines(lines) {
  return (lines ?? []).map(parseReportLine).filter(Boolean);
}

export function splitByInit(reports) {
  const cold = (reports ?? []).filter((report) => Number.isFinite(report.initDurationMs));
  const warm = (reports ?? []).filter((report) => !Number.isFinite(report.initDurationMs));
  return { cold, warm };
}

export function reportCountProblem({ coldReports, coldSamples }) {
  if (coldReports === coldSamples) return null;
  return (
    `${coldReports} REPORT line(s) carry an Init Duration but ${coldSamples} cold sample(s) were driven; ` +
    `the cold numbers below are correlated by time window, so a mismatch means some of them are not the ` +
    `invocations that were measured`
  );
}

export function fetchReportLines({ functionName, logGroupName, region, startMs, endMs }) {
  const group = logGroupName || logGroupFor(functionName);
  const lines = [];
  let nextToken = null;
  for (;;) {
    const response = JSON.parse(
      aws(
        [
          "logs",
          "filter-log-events",
          "--log-group-name",
          group,
          "--start-time",
          String(Math.floor(startMs)),
          "--end-time",
          String(Math.ceil(endMs)),
          "--filter-pattern",
          REPORT_FILTER_PATTERN,
          "--limit",
          String(LOG_PAGE_LIMIT),
          ...(nextToken ? ["--next-token", nextToken] : []),
          "--output",
          "json",
        ],
        { region },
      ),
    );
    for (const event of response.events ?? []) {
      lines.push(String(event.message ?? ""));
    }
    nextToken = response.nextToken ?? null;
    if (!nextToken) return lines;
  }
}
