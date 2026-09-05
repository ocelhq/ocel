import { spawn } from "node:child_process";
import { mkdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { type Run, settleAccount } from "./account";
import type { ExpectationEnvironment } from "./expectations";
import { currentRunIdentity } from "./identity";
import { follow, LIVE_ENV } from "./live";
import { longestFirst } from "./order";
import { cellFile, cellFilesDir, cellsDir, liveFile, packageRoot, prepareFile } from "./paths";
import { pickFixtures, requestedPick } from "./pick";
import type { PrepareFailures } from "./prepare";
import {
  CONCERN_ENV,
  ENVIRONMENT_ENV,
  FIXTURES_ENV,
  fixturesFor,
  type Selection,
  selectionFor,
} from "./selection";
import type { Cell, FixtureSpec } from "./spec";
import { fixtureNameOf, groupKeyOf } from "./spec";
import { laneWorkers, selectedTarget } from "./targets";
import type { Target } from "./targets/types";

const RUN_CELL = path.join(packageRoot, "src", "runCell.ts");

export function cellFileSource(cell: Cell): string {
  return `import { describeCell } from ${JSON.stringify(RUN_CELL)};\n\ndescribeCell(${JSON.stringify(cell.name)});\n`;
}

async function writeCellFiles(runId: string, target: Target, cells: Cell[]): Promise<string[]> {
  const dir = cellFilesDir(runId, target.name);
  await rm(dir, { recursive: true, force: true });
  await mkdir(dir, { recursive: true });
  const files: string[] = [];
  for (const cell of cells) {
    const file = cellFile(runId, target.name, cell.name);
    await writeFile(file, cellFileSource(cell), "utf8");
    files.push(path.relative(packageRoot, file));
  }
  return files;
}

async function runSuite(
  target: Target,
  files: string[],
  workers: number,
  env: NodeJS.ProcessEnv,
  live: string,
): Promise<Run> {
  await rm(live, { force: true });
  const stop = follow(live);
  const child = spawn(
    "bun",
    ["test", `--parallel=${workers}`, `--timeout=${target.legTimeoutMs}`, ...files],
    { cwd: packageRoot, stdio: "inherit", env: { ...env, [LIVE_ENV]: live } },
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

function sayWhatIsSkipped(target: Target, selection: Selection) {
  for (const [cell, listed] of Object.entries(selection.skipped)) {
    const why = listed.map((gap) => (gap.issue === undefined ? gap.id : `#${gap.issue}`));
    process.stderr.write(`${target.name}: skipping ${cell} (${why.join(", ")})\n`);
  }
}

export async function runJourney(
  target: Target,
  fixtures: FixtureSpec[],
  leftOut: FixtureSpec[] = [],
): Promise<number> {
  const runId = currentRunIdentity();
  await rm(cellsDir(runId, target.name), { recursive: true, force: true });

  const environment: ExpectationEnvironment = await target.guard();
  const env: NodeJS.ProcessEnv = {
    ...process.env,
    OCEL_TARGET: target.name,
    [CONCERN_ENV]: [...new Set(fixtures.map((row) => row.concern))].join(" "),
    [FIXTURES_ENV]: fixtures.map(fixtureNameOf).join(","),
    OCEL_JOURNEY_LEFT_OUT: leftOut.map(fixtureNameOf).join(","),
    [ENVIRONMENT_ENV]: environment,
  };
  const selection = selectionFor(target, environment, env);
  sayWhatIsSkipped(target, selection);
  const files = await writeCellFiles(runId, target, longestFirst(selection.cells));
  const workers = laneWorkers(target);

  await prepareLane(target, runId);
  const runStart = Date.now();
  const run =
    files.length === 0
      ? { exitCode: 0, signal: null }
      : await runSuite(target, files, workers, env, liveFile(runId, target.name));
  const runEnd = Date.now();

  const verdict = await settleAccount({
    target,
    environment,
    env,
    run,
    runStart,
    runEnd,
    workers,
  });
  if (verdict.exitCode !== 0) {
    process.stderr.write(`\nthe journey account does not reconcile:\n${verdict.report}\n`);
  }
  return verdict.exitCode;
}

function sayWhatIsLeftOut(seed: string, chosen: FixtureSpec[], leftOut: FixtureSpec[]) {
  const groups = new Set(leftOut.map(groupKeyOf));
  for (const group of groups) {
    const running = chosen.filter((row) => groupKeyOf(row) === group).map(fixtureNameOf);
    const dropped = leftOut.filter((row) => groupKeyOf(row) === group).map(fixtureNameOf);
    process.stderr.write(
      `${group}: running ${running.join(", ")}, leaving out ${dropped.join(", ")} (seed ${seed})\n`,
    );
  }
}

async function main(): Promise<number> {
  const target = selectedTarget();
  const named = fixturesFor(target.name, process.env);
  const pick = requestedPick();
  const { chosen, leftOut } = pickFixtures(named, pick);
  if (pick && leftOut.length > 0) {
    sayWhatIsLeftOut(pick.seed, chosen, leftOut);
  }
  return runJourney(target, chosen, leftOut);
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
