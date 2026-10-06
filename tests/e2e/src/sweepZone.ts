import { fixtures } from "./matrix/fixtures";
import { runSweep } from "./sweepArgs";
import { targetNamed } from "./targets";
import { githubRuns } from "./targets/aws/runs";
import { CloudflareApi } from "./zone/cloudflare";
import { sweepRunFromZone, sweepStaleFromZone, zoneCellsOn } from "./zone/sweep";

function required(name: string, use: string): string {
  const value = process.env[name]?.trim();
  if (!value) {
    throw new Error(`${name} is unset, and it is ${use}`);
  }
  return value;
}

runSweep(async (args) => {
  const target = targetNamed(args.target).name;
  const token = required("CLOUDFLARE_API_TOKEN", "the token the zone's records are deleted with");
  const account = required("CLOUDFLARE_ACCOUNT_ID", "the account the zone is looked up in");
  const zoneName = required("OCEL_E2E_ZONE", "the zone the journey wrote its hostnames in");
  const api = new CloudflareApi(token);
  const zone = await api.readZone(zoneName, account);
  const cells = zoneCellsOn(fixtures, target);
  await (args.oneRun
    ? sweepRunFromZone(api, zone, cells, args.runId)
    : sweepStaleFromZone(api, zone, cells, args.runId, githubRuns(process.env)));
});
