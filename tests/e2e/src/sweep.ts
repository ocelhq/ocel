import { runSweep } from "./sweepArgs";
import { targetNamed } from "./targets";

runSweep(async (args) => {
  const target = targetNamed(args.target);
  await target.detectLane();
  await (args.oneRun ? target.sweeper.sweepRun(args.runId) : target.sweeper.sweepStale(args.runId));
});
