import { spawn } from "node:child_process";
import { mkdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { type Run, settleAccount } from "./account";
import { currentRunIdentity } from "./identity";
import { longestFirst } from "./order";
import { cellsDir, packageRoot, prepareFile } from "./paths";
import { pickExamples, requestedPick } from "./pick";
import type { PrepareFailures } from "./prepare";
import { type ExampleSpec, examplesNamed, specForTarget } from "./spec";
import { laneWorkers, selectedTarget } from "./targets";
import type { Target } from "./targets/types";

function runSuite(
  target: Target,
  examples: ExampleSpec[],
  workers: number,
  env: NodeJS.ProcessEnv,
): Promise<Run> {
  const child = spawn(
    "bun",
    [
      "test",
      `--parallel=${workers}`,
      `--timeout=${target.legTimeoutMs}`,
      ...longestFirst(examples).map((example) => `./tests/${example.name}.journey.test.ts`),
    ],
    { cwd: packageRoot, stdio: "inherit", env },
  );
  return new Promise<Run>((resolve) => {
    child.on("close", (exitCode, signal) => resolve({ exitCode, signal }));
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

export async function runJourney(
  target: Target,
  examples: ExampleSpec[],
  leftOut: ExampleSpec[] = [],
): Promise<number> {
  const runId = currentRunIdentity();
  await rm(cellsDir(runId, target.name), { recursive: true, force: true });

  const env: NodeJS.ProcessEnv = {
    ...process.env,
    OCEL_TARGET: target.name,
    OCEL_EXAMPLES: examples.map((row) => row.name).join(","),
    OCEL_JOURNEY_LEFT_OUT: leftOut.map((row) => row.name).join(","),
  };
  const workers = laneWorkers(target);

  await prepareLane(target, runId);
  const runStart = Date.now();
  const run = await runSuite(target, examples, workers, env);
  const runEnd = Date.now();

  const verdict = await settleAccount({ target, env, run, runStart, runEnd, workers });
  if (verdict.exitCode !== 0) {
    process.stderr.write(`\nthe journey account does not reconcile:\n${verdict.report}\n`);
  }
  return verdict.exitCode;
}

function sayWhatIsLeftOut(seed: string, chosen: ExampleSpec[], leftOut: ExampleSpec[]) {
  const groups = new Set(leftOut.map((row) => row.group));
  for (const group of groups) {
    const running = chosen.filter((row) => row.group === group).map((row) => row.name);
    const dropped = leftOut.filter((row) => row.group === group).map((row) => row.name);
    process.stderr.write(
      `${group}: running ${running.join(", ")}, leaving out ${dropped.join(", ")} (seed ${seed})\n`,
    );
  }
}

async function main(): Promise<number> {
  const target = selectedTarget();
  const named = examplesNamed(specForTarget(target.name), process.env.OCEL_EXAMPLES);
  const pick = requestedPick();
  const { chosen, leftOut } = pickExamples(named, pick);
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
