import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { outputRoot } from "../../paths";
import { accountFiles, pinnedEnv, PROFILE_VARS } from "./account";
import { answersAsFloci, awsStore } from "./store";
import { detectWorld, type Where } from "./world";

const FLOCI_ZONE = "journey.test";

const pinnedDir = path.join(outputRoot, "aws-account");

let placed: Promise<Where> | undefined;

async function pinAccountFiles(): Promise<void> {
  await mkdir(pinnedDir, { recursive: true });
  const { config, credentials } = accountFiles(pinnedDir);
  await writeFile(config, "", "utf8");
  await writeFile(credentials, "", "utf8");
  const pinned = pinnedEnv(process.env, pinnedDir);
  for (const name of PROFILE_VARS) {
    delete process.env[name];
  }
  Object.assign(process.env, pinned);
}

export async function place(): Promise<Where> {
  placed ??= (async () => {
    await pinAccountFiles();
    const where = await detectWorld(process.env, {
      answersAsFloci,
      callerAccount: () => awsStore(process.env.AWS_ENDPOINT_URL).callerAccount(),
    });
    if (where.world === "floci") {
      process.env.AWS_ACCESS_KEY_ID ??= "test";
      process.env.AWS_SECRET_ACCESS_KEY ??= "test";
      process.env.OCEL_JOURNEY_ZONE ??= FLOCI_ZONE;
    } else {
      if (!process.env.OCEL_JOURNEY_ZONE) {
        throw new Error(
          "OCEL_JOURNEY_ZONE names the zone this run's production hostnames hang under, and an aws project with no production hostname has nowhere to serve",
        );
      }
      process.env.OCEL_JOURNEY_DNS = "cloudflare";
    }
    return where;
  })();
  return placed;
}
