import type { TargetName } from "../matrix/types";
import type { RunFilter } from "../plan";
import { targetNamed } from "../targets";
import { runJourney } from "./journey";

export type Smoke = {
  target: TargetName;
  fixture: string;
  variant: string;
  keep: boolean;
};

export const SMOKES: Record<string, Smoke> = {
  aws: { target: "aws", fixture: "deploy/node", variant: "api-gateway", keep: false },
  gcp: { target: "gcp", fixture: "deploy/node", variant: "default", keep: false },
  vps: { target: "vps", fixture: "deploy/node", variant: "default", keep: false },
  dev: { target: "dev", fixture: "deploy/node", variant: "default", keep: false },
  next: { target: "aws", fixture: "deploy/next", variant: "cloudflare", keep: true },
};

const USAGE = `pnpm --filter @ocel-tests/e2e smoke --target <${Object.keys(SMOKES).join("|")}>`;

export function smokeNamed(name: string | undefined): Smoke {
  const smoke = name === undefined ? undefined : SMOKES[name];
  if (!smoke) {
    throw new Error(`no smoke named ${name ?? "(none)"}\n${USAGE}`);
  }
  return smoke;
}

export function smokeFilter(smoke: Smoke): RunFilter {
  return {
    concerns: ["deploy"],
    fixtures: [smoke.fixture],
    variants: [smoke.variant],
    coverage: "every-cell",
    runSkipped: false,
    keep: smoke.keep,
  };
}

function targetFlag(argv: string[]): string | undefined {
  const index = argv.indexOf("--target");
  return index === -1 ? undefined : argv[index + 1];
}

async function main(argv: string[]): Promise<number> {
  const smoke = smokeNamed(targetFlag(argv));
  return runJourney(targetNamed(smoke.target), smokeFilter(smoke));
}

if (import.meta.main) {
  main(process.argv.slice(2)).then(
    (code) => {
      process.exitCode = code;
    },
    (error) => {
      process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
      process.exitCode = 1;
    },
  );
}
