import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import type { Lane } from "../../matrix/types";
import { outputRoot } from "../../paths";
import { accountFiles, PROFILE_VARS, pinnedEnv } from "./account";
import { answersAsFloci, awsStore } from "./store";

export type World = "floci" | "real";

export const ENDPOINT_ENV = "AWS_ENDPOINT_URL";
export const ACCOUNT_ENV = "EXPECTED_AWS_ACCOUNT_ID";

export const START_FLOCI =
  "scripts/floci.sh create ocel-journeys, then export the OCEL_FLOCI_ENDPOINT it prints as AWS_ENDPOINT_URL";

export type Probes = {
  answersAsFloci: (endpoint: string) => Promise<boolean>;
  callerAccount: () => Promise<string>;
};

export type Where = { world: World; endpoint?: string };

const FLOCI_ZONE = "journey.test";

const PINNED_DIR = path.join(outputRoot, "aws-account");

export function emulatorEndpoint(env: NodeJS.ProcessEnv): string | undefined {
  return env[ENDPOINT_ENV]?.trim() || undefined;
}

export async function detectWorld(env: NodeJS.ProcessEnv, probes: Probes): Promise<Where> {
  const endpoint = emulatorEndpoint(env);
  if (endpoint) {
    if (!(await probes.answersAsFloci(endpoint))) {
      throw new Error(
        `${ENDPOINT_ENV} is ${endpoint} and nothing there answers as floci; the journey harness never starts an emulator. Run: ${START_FLOCI}`,
      );
    }
    return { world: "floci", endpoint };
  }
  const expected = env[ACCOUNT_ENV]?.trim();
  if (!expected) {
    throw new Error(
      `nothing says which aws this run drives: set ${ENDPOINT_ENV} to a floci emulator, or ${ACCOUNT_ENV} to the account a dispatch run may spend. Run: ${START_FLOCI}`,
    );
  }
  const resolved = await probes.callerAccount();
  if (resolved !== expected) {
    throw new Error(
      `these credentials resolve to aws account ${resolved} and this run may only touch ${expected} — refusing to deploy`,
    );
  }
  return { world: "real" };
}

export function laneOf(world: World): Lane {
  return world === "floci" ? "aws.floci" : "aws";
}

async function pinAccountFiles(): Promise<void> {
  await mkdir(PINNED_DIR, { recursive: true });
  const { config, credentials } = accountFiles(PINNED_DIR);
  await writeFile(config, "", "utf8");
  await writeFile(credentials, "", "utf8");
  const pinned = pinnedEnv(process.env, PINNED_DIR);
  for (const name of PROFILE_VARS) {
    delete process.env[name];
  }
  Object.assign(process.env, pinned);
}

export class AwsWorld {
  private detected: Promise<Where> | undefined;

  detect(): Promise<Where> {
    this.detected ??= (async () => {
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
    return this.detected;
  }

  async lane(): Promise<Lane> {
    return laneOf((await this.detect()).world);
  }

  async real(): Promise<boolean> {
    return (await this.detect()).world === "real";
  }

  async endpoint(): Promise<string | undefined> {
    return (await this.detect()).endpoint;
  }

  zone(): string {
    const named = process.env.OCEL_JOURNEY_ZONE;
    if (!named) {
      throw new Error("the aws target reached a cell before it knew which zone to serve on");
    }
    return named;
  }
}

export const awsWorld = new AwsWorld();
