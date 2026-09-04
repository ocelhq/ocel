import { spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { readFile, rm } from "node:fs/promises";
import { currentRunIdentity } from "./identity";
import { verdictFile } from "./paths";
import { type Pick, pickExamples } from "./pick";
import { type ExampleSpec, examplesNamed, specForTarget } from "./spec";
import { selectedTarget } from "./targets";
import type { Target } from "./targets/types";

export type Verdict = { nonce: string; exitCode: number; report: string };

export async function runJourney(
  target: Target,
  examples: ExampleSpec[],
  leftOut: ExampleSpec[] = [],
): Promise<number> {
  const runId = currentRunIdentity();
  const file = verdictFile(runId, target.name);
  const nonce = randomUUID();
  await rm(file, { force: true });

  const child = spawnSync(
    "pnpm",
    ["vitest", "run", ...examples.map((row) => `tests/${row.name}.journey.test.ts`)],
    {
      stdio: "inherit",
      env: {
        ...process.env,
        OCEL_TARGET: target.name,
        OCEL_EXAMPLES: examples.map((row) => row.name).join(","),
        OCEL_JOURNEY_LEFT_OUT: leftOut.map((row) => row.name).join(","),
        OCEL_JOURNEY_VERDICT_NONCE: nonce,
      },
    },
  );
  if (child.signal) {
    process.stderr.write(`\nvitest was killed by ${child.signal}\n`);
    return 1;
  }
  if (child.status !== 0 && child.status !== 1) {
    process.stderr.write(`\nvitest exited ${child.status}, which is not a test result\n`);
    return 1;
  }

  let verdict: Verdict;
  try {
    verdict = JSON.parse(await readFile(file, "utf8")) as Verdict;
  } catch {
    process.stderr.write(
      `\nthe run wrote no account at ${file}: the journey never reconciled and the lane is red\n`,
    );
    return 1;
  }
  if (verdict.nonce !== nonce) {
    process.stderr.write(`\nthe account at ${file} was written by another run\n`);
    return 1;
  }
  if (verdict.exitCode !== 0) {
    process.stderr.write(`\n${verdict.report}\n`);
  }
  return verdict.exitCode;
}

function requestedPick(): Pick | undefined {
  const seed = (process.env.OCEL_JOURNEY_SEED ?? "").trim();
  if (seed === "") {
    return undefined;
  }
  return {
    seed,
    touched: (process.env.OCEL_JOURNEY_TOUCHED ?? "")
      .split(",")
      .map((dir) => dir.trim())
      .filter((dir) => dir !== ""),
  };
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

if (import.meta.url === `file://${process.argv[1]}`) {
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
