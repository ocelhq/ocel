export type SweepAsk = { target: string; runId: string; own: boolean };

export const SWEEP_USAGE = "pnpm sweep --target <name> [--own] [--run <id>]";

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

export function sweepAsk(argv: string[], current: string): SweepAsk {
  const target = flag(argv, "target");
  if (!target) {
    throw new Error(SWEEP_USAGE);
  }
  const named = flag(argv, "run");
  return { target, runId: named ?? current, own: named !== undefined || argv.includes("--own") };
}
