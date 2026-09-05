import { runJourney } from "./journey";
import { parseShard } from "./shard";
import { type Concern, concernsAsked, specByName } from "./spec";
import { targetNamed } from "./targets";

const USAGE =
  "pnpm cell --concern <name> --fixture <name> --target <name> [--shard <index>/<total>]";

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

function oneConcern(asked: string): Concern {
  const named = concernsAsked(asked);
  if (named.length !== 1) {
    throw new Error(`--concern names one concern, not ${named.join(" and ")}\n${USAGE}`);
  }
  return named[0];
}

async function main(argv: string[]): Promise<number> {
  const concernName = flag(argv, "concern");
  const fixtureName = flag(argv, "fixture");
  const targetName = flag(argv, "target");
  if (!concernName || !fixtureName || !targetName) {
    throw new Error(USAGE);
  }
  parseShard(flag(argv, "shard"));

  const fixture = specByName(oneConcern(concernName), fixtureName);
  const target = targetNamed(targetName);
  if (fixture.targets && !fixture.targets.includes(target.name)) {
    throw new Error(`the ${fixture.dir} fixture does not run on ${target.name}`);
  }

  return runJourney(target, [fixture]);
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
