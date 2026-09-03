import { spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { readFile, rm } from "node:fs/promises";
import { currentRunIdentity } from "./identity";
import { verdictFile } from "./paths";
import { type ExampleSpec, specForTarget } from "./spec";
import { selectedTarget } from "./targets";
import type { Target } from "./targets/types";

export type Verdict = { nonce: string; exitCode: number; report: string };

export async function runJourney(target: Target, examples: ExampleSpec[]): Promise<number> {
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

if (import.meta.url === `file://${process.argv[1]}`) {
  const target = selectedTarget();
  runJourney(target, specForTarget(target.name)).then(
    (code) => {
      process.exitCode = code;
    },
    (error) => {
      process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
      process.exitCode = 1;
    },
  );
}
