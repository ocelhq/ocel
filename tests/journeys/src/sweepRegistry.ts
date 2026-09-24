import { fixtures } from "./matrix/fixtures";
import { reclaimRegistry } from "./registry/github";
import { registryPackages } from "./registry/packages";

const USAGE = "pnpm sweep:registry --since <when the lane began pushing, ISO 8601>";

async function main(argv: string[]): Promise<void> {
  const at = argv.indexOf("--since");
  const since = new Date(at === -1 ? Number.NaN : (argv[at + 1] ?? Number.NaN));
  if (Number.isNaN(since.getTime())) {
    throw new Error(USAGE);
  }
  const token = process.env.GH_TOKEN;
  if (!token) {
    throw new Error("GH_TOKEN carries the token the journey registry's versions are deleted with");
  }
  await reclaimRegistry(registryPackages(fixtures), since, token);
}

main(process.argv.slice(2)).then(
  () => {},
  (error: unknown) => {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 1;
  },
);
