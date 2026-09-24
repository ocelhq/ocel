import { currentRunIdentity } from "./identity";

export type SweepArgs = { target: string; runId: string; oneRun: boolean };

export const SWEEP_USAGE = "pnpm sweep[:registry] --target <name> [--own] [--run <id>]";

function flag(argv: string[], name: string): string | undefined {
  const index = argv.indexOf(`--${name}`);
  if (index === -1) {
    return undefined;
  }
  const value = argv[index + 1];
  if (value === undefined || value.startsWith("--")) {
    throw new Error(`--${name} needs a value\n${SWEEP_USAGE}`);
  }
  return value;
}

export function sweepArgs(argv: string[], current: string): SweepArgs {
  const target = flag(argv, "target");
  if (!target) {
    throw new Error(SWEEP_USAGE);
  }
  const named = flag(argv, "run");
  return { target, runId: named ?? current, oneRun: named !== undefined || argv.includes("--own") };
}

export function runSweep(sweep: (args: SweepArgs) => Promise<void>): void {
  Promise.resolve()
    .then(() => sweep(sweepArgs(process.argv.slice(2), currentRunIdentity())))
    .then(
      () => {},
      (error: unknown) => {
        process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
        process.exitCode = 1;
      },
    );
}
