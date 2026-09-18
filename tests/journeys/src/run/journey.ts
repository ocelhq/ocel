import { spawn } from "node:child_process";
import { mkdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { currentRunIdentity } from "../identity";
import { follow, LIVE_ENV } from "../live";
import { fixtures } from "../matrix/fixtures";
import { gaps } from "../matrix/gaps";
import {
  cellFile,
  cellFilesDir,
  cellsDir,
  liveFile,
  packageRoot,
  planFile,
  prepareFile,
} from "../paths";
import { type Ask, type Plan, plan } from "../plan";
import type { PrepareFailures } from "../prepare";
import { type Run, settleAccount } from "../report/account";
import { laneWorkers, selectedTarget } from "../targets";
import type { Target } from "../targets/types";
import { askFrom } from "./ask";

const DESCRIBE_CELL = path.join(packageRoot, "src", "run", "describeCell.ts");

function cellFileSource(plan: string, cell: string): string {
  return `import { describeCell } from ${JSON.stringify(DESCRIBE_CELL)};\n\ndescribeCell(${JSON.stringify(plan)}, ${JSON.stringify(cell)});\n`;
}

async function writePlan(runId: string, target: Target, planned: Plan): Promise<string> {
  const file = planFile(runId, target.name);
  await mkdir(path.dirname(file), { recursive: true });
  await writeFile(file, `${JSON.stringify(planned, null, 2)}\n`, "utf8");
  return file;
}

async function writeCellFiles(runId: string, target: Target, planned: Plan): Promise<string[]> {
  const plan = await writePlan(runId, target, planned);
  const dir = cellFilesDir(runId, target.name);
  await rm(dir, { recursive: true, force: true });
  await mkdir(dir, { recursive: true });
  const files: string[] = [];
  for (const cell of planned.cells) {
    const file = cellFile(runId, target.name, cell.name);
    await writeFile(file, cellFileSource(plan, cell.name), "utf8");
    files.push(path.relative(packageRoot, file));
  }
  return files;
}

async function runSuite(target: Target, files: string[], workers: number, live: string) {
  await rm(live, { force: true });
  const stop = follow(live);
  const child = spawn(
    "bun",
    ["test", `--parallel=${workers}`, `--timeout=${target.legTimeoutMs}`, ...files],
    { cwd: packageRoot, stdio: "inherit", env: { ...process.env, [LIVE_ENV]: live } },
  );
  return new Promise<Run>((resolve) => {
    child.on("close", (exitCode, signal) => {
      stop();
      resolve({ exitCode, signal });
    });
  });
}

async function prepareLane(target: Target, runId: string): Promise<void> {
  const began = Date.now();
  let failures: PrepareFailures = {};
  try {
    failures = (await target.prepare?.()) ?? {};
  } catch (error) {
    failures = { lane: error instanceof Error ? error.message : String(error) };
  }
  const file = prepareFile(runId, target.name);
  await mkdir(path.dirname(file), { recursive: true });
  await writeFile(file, `${JSON.stringify({ ms: Date.now() - began, failures })}\n`, "utf8");
}

function sayWhatIsSkipped(target: Target, planned: Plan) {
  for (const [cell, listed] of Object.entries(planned.skipped)) {
    const why = listed.map((gap) => (gap.issue === undefined ? gap.id : `#${gap.issue}`));
    process.stderr.write(`${target.name}: skipping ${cell} (${why.join(", ")})\n`);
  }
}

export async function runJourney(target: Target, ask: Ask): Promise<number> {
  const runId = currentRunIdentity();
  await rm(cellsDir(runId, target.name), { recursive: true, force: true });

  const lane = await target.guard();
  const planned = plan({ fixtures, gaps, lane, legs: target.legs, ask });
  sayWhatIsSkipped(target, planned);
  const files = await writeCellFiles(runId, target, planned);
  const workers = laneWorkers(target);

  const idle = files.length === 0;
  if (!idle) {
    await prepareLane(target, runId);
  }
  const runStart = Date.now();
  const run: Run = idle
    ? { exitCode: 0, signal: null }
    : await runSuite(target, files, workers, liveFile(runId, target.name));
  const runEnd = Date.now();

  const verdict = await settleAccount({ target, plan: planned, run, runStart, runEnd, workers });
  if (verdict.exitCode !== 0) {
    process.stderr.write(`\nthe journey account does not reconcile:\n${verdict.report}\n`);
  }
  return verdict.exitCode;
}

async function main(): Promise<number> {
  return runJourney(selectedTarget(), askFrom(process.env));
}

if (import.meta.main) {
  main().then(
    (code) => {
      process.exitCode = code;
    },
    (error) => {
      process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
      process.exitCode = 1;
    },
  );
}
