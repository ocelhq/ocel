import { currentRunIdentity } from "./identity";
import { sweepArgs } from "./sweepArgs";
import { targetNamed } from "./targets";

async function main(argv: string[]): Promise<void> {
  const args = sweepArgs(argv, currentRunIdentity());
  const target = targetNamed(args.target);
  await target.detectLane();
  await (args.oneRun ? target.sweeper.sweepRun(args.runId) : target.sweeper.sweepStale(args.runId));
}

main(process.argv.slice(2)).then(
  () => {},
  (error: unknown) => {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 1;
  },
);
