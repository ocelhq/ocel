import { runJourney } from "./journey";
import { parseShard } from "./shard";
import { specByName } from "./spec";
import { targetNamed } from "./targets";

const USAGE = "pnpm cell --example <name> --target <name> [--shard <index>/<total>]";

function flag(argv: string[], name: string): string | undefined {
  const index = argv.indexOf(`--${name}`);
  if (index === -1) {
    return undefined;
  }
  const value = argv[index + 1];
  if (value === undefined || value.startsWith("--")) {
    throw new Error(`--${name} needs a value\n${USAGE}`);
  }
  return value;
}

async function main(argv: string[]): Promise<number> {
  const exampleName = flag(argv, "example");
  const targetName = flag(argv, "target");
  if (!exampleName || !targetName) {
    throw new Error(USAGE);
  }
  parseShard(flag(argv, "shard"));

  const example = specByName(exampleName);
  const target = targetNamed(targetName);
  if (example.targets && !example.targets.includes(target.name)) {
    throw new Error(`the ${example.name} example does not run on ${target.name}`);
  }

  return runJourney(target, [example]);
}

main(process.argv.slice(2)).then(
  (code) => {
    process.exitCode = code;
  },
  (error) => {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 1;
  },
);
