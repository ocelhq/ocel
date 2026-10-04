import { fixtures } from "./matrix/fixtures";
import { deletePackages } from "./registry/github";
import { registryPackages } from "./registry/packages";
import { readResults } from "./run/results";
import { runSweep } from "./sweepArgs";
import { targetNamed } from "./targets";

runSweep(async (args) => {
  const token = process.env.GH_TOKEN;
  if (!token) {
    throw new Error("GH_TOKEN is the token the journey registry's packages are deleted with");
  }
  const target = targetNamed(args.target).name;
  const results = args.oneRun ? await readResults(args.runId, target) : [];
  await deletePackages(registryPackages(fixtures, target, results), token);
});
