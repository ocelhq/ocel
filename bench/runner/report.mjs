import { mkdirSync, writeFileSync } from "node:fs";
import { dirname } from "node:path";

import { PERCENTILE_METHOD, summarize } from "./stats.mjs";

const COLUMNS = Object.freeze([
  { key: "platform", label: "platform", align: "left" },
  { key: "deploy", label: "deploy s" },
  { key: "build", label: "build s" },
  { key: "provision", label: "provision s" },
  { key: "coldP50", label: "cold p50" },
  { key: "coldP99", label: "cold p99" },
  { key: "initP50", label: "init p50" },
  { key: "warmP50", label: "warm p50" },
  { key: "warmP99", label: "warm p99" },
  { key: "durP50", label: "dur p50" },
  { key: "note", label: "note", align: "left" },
]);

export function summarizeCell(cell) {
  const cold = cell.measurement?.cold;
  const warm = cell.measurement?.warm;
  return {
    rttCold: summarize((cold?.samples ?? []).map((sample) => sample.rttMs)),
    rttWarm: summarize((warm?.samples ?? []).map((sample) => sample.rttMs)),
    initDuration: summarize((cold?.reports ?? []).map((report) => report.initDurationMs)),
    lambdaDuration: summarize((warm?.reports ?? []).map((report) => report.durationMs)),
  };
}

export function cellRow(cell, samples) {
  const stats = summarizeCell(cell);
  return {
    platform: cell.platform,
    deploy: seconds(cell.deploy?.wallMs),
    build: seconds(cell.deploy?.buildMs),
    provision: seconds(cell.deploy?.provisionMs),
    coldP50: millis(stats.rttCold.p50),
    coldP99: millis(stats.rttCold.p99),
    initP50: millis(stats.initDuration.p50),
    warmP50: millis(stats.rttWarm.p50),
    warmP99: millis(stats.rttWarm.p99),
    durP50: millis(stats.lambdaDuration.p50),
    note: note(cell, samples),
  };
}

function note(cell, samples) {
  if (cell.status === "failed") return `FAILED ${firstLine(cell.error)}`;
  if (cell.status === "skipped") return `SKIPPED ${firstLine(cell.error)}`;
  const marks = [];
  const warm = cell.measurement?.warm?.samples?.length ?? 0;
  const cold = cell.measurement?.cold?.samples?.length ?? 0;
  if (samples && (warm < samples.warm || cold < samples.cold)) {
    marks.push(`partial: ${cold}/${samples.cold} cold, ${warm}/${samples.warm} warm`);
  }
  if (cell.measurement?.errors?.length) marks.push(`${cell.measurement.errors.length} bad sample(s)`);
  if (cell.measurement?.warnings?.length) marks.push(`${cell.measurement.warnings.length} warning(s)`);
  return marks.join(", ");
}

const NOTE_MAX = 72;

function firstLine(text) {
  const line = String(text ?? "").split("\n")[0];
  return line.length > NOTE_MAX ? `${line.slice(0, NOTE_MAX - 1)}…` : line;
}

function seconds(ms) {
  return Number.isFinite(ms) ? (ms / 1000).toFixed(1) : "-";
}

function millis(ms) {
  return Number.isFinite(ms) ? ms.toFixed(1) : "-";
}

export function renderTable({ cells, pinned, region, samples }) {
  const lines = [
    `ocel node-framework deploy benchmark`,
    `  ${pinned.runtime}, ${pinned.memoryMB} MB, ${pinned.architecture}, ${region}`,
    `  ${samples.cold} cold sample(s), ${samples.warm} warm sample(s), ${samples.warmup} warm-up(s) discarded, ` +
      `${Math.round(samples.logSettleMs / 1000)}s log settle`,
    `  latencies in ms, ${PERCENTILE_METHOD}; cold/warm p* are client RTT, init/dur are the lambda REPORT line`,
    ``,
  ];

  for (const framework of [...new Set(cells.map((cell) => cell.app))]) {
    const rows = cells.filter((cell) => cell.app === framework).map((cell) => cellRow(cell, samples));
    lines.push(framework, ...renderRows(rows).map((line) => `  ${line}`), ``);
  }

  const warnings = cells.flatMap((cell) =>
    (cell.measurement?.warnings ?? []).map((warning) => `  ${cell.id}: ${warning}`),
  );
  if (warnings.length > 0) {
    lines.push(`warnings`, ...warnings, ``);
  }

  return lines.join("\n");
}

function renderRows(rows) {
  const widths = COLUMNS.map((column) =>
    Math.max(column.label.length, ...rows.map((row) => String(row[column.key] ?? "").length)),
  );
  const render = (cells) =>
    COLUMNS.map((column, index) =>
      column.align === "left" ? cells[index].padEnd(widths[index]) : cells[index].padStart(widths[index]),
    )
      .join("  ")
      .trimEnd();
  return [
    render(COLUMNS.map((column) => column.label)),
    render(widths.map((width) => "-".repeat(width))),
    ...rows.map((row) => render(COLUMNS.map((column) => String(row[column.key] ?? "")))),
  ];
}

export function resultsPayload({ cells, pinned, region, samples, startedAt, finishedAt, aborted }) {
  return {
    startedAt: new Date(startedAt).toISOString(),
    finishedAt: new Date(finishedAt).toISOString(),
    aborted: Boolean(aborted),
    region,
    pinned,
    samples,
    percentileMethod: PERCENTILE_METHOD,
    cells: cells.map((cell) => ({ ...cell, summary: summarizeCell(cell) })),
  };
}

export function writeResults(path, payload) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, `${JSON.stringify(payload, null, 2)}\n`);
  return path;
}
