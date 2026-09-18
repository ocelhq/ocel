import { parseShard } from "../shard";
import { targetNamed } from "../targets";
import { askFrom, concernsNamed } from "./ask";
import { runJourney } from "./journey";

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

async function main(argv: string[]): Promise<number> {
  const concernName = flag(argv, "concern");
  const fixtureName = flag(argv, "fixture");
  const targetName = flag(argv, "target");
  if (!concernName || !fixtureName || !targetName) {
    throw new Error(USAGE);
  }
  parseShard(flag(argv, "shard"));

  const [concern, ...more] = concernsNamed(concernName);
  if (!concern || more.length > 0) {
    throw new Error(`--concern names one concern, not ${concernName}\n${USAGE}`);
  }
  return runJourney(targetNamed(targetName), {
    ...askFrom(process.env),
    concerns: [concern],
    fixtures: [`${concern}/${fixtureName}`],
  });
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
