import { currentRunIdentity } from "./identity";
import { targetNamed } from "./targets";

const USAGE = "pnpm sweep --target <name>";

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

async function main(argv: string[]): Promise<void> {
  const targetName = flag(argv, "target");
  if (!targetName) {
    throw new Error(USAGE);
  }
  const target = targetNamed(targetName);
  await target.guard();
  await target.sweep(currentRunIdentity());
}

main(process.argv.slice(2)).then(
  () => {},
  (error: unknown) => {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 1;
  },
);
