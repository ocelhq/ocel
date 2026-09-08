import { currentRunIdentity } from "./identity";
import { sweepAsk } from "./sweepArgs";
import { targetNamed } from "./targets";

async function main(argv: string[]): Promise<void> {
  const ask = sweepAsk(argv, currentRunIdentity());
  const target = targetNamed(ask.target);
  await target.guard();
  await (ask.own ? target.sweepOwn(ask.runId) : target.sweep(ask.runId));
}

main(process.argv.slice(2)).then(
  () => {},
  (error: unknown) => {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 1;
  },
);
