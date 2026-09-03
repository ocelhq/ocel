import type { ExpectationEnvironment } from "../../expectations/types";

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

export async function detectWorld(env: NodeJS.ProcessEnv, probes: Probes): Promise<Where> {
  const endpoint = env[ENDPOINT_ENV]?.trim();
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

export function expectationEnvironmentFor(world: World): ExpectationEnvironment {
  return world === "floci" ? "aws.floci" : "aws";
}
