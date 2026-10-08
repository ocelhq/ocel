import { stripVTControlCharacters } from "node:util";
import { NonzeroExitError, type Ran } from "../ocel";
import type { OcelBuild } from "../paths";

export const BOOTSTRAP_BIN_ENV = "OCEL_E2E_BOOTSTRAP_BIN";
export const BOOTSTRAP_PROVIDERS_DIR_ENV = "OCEL_E2E_BOOTSTRAP_PROVIDERS_DIR";

const BOOTSTRAP_COMMAND = "`ocel bootstrap production";
const BEHIND = /bootstrap is behind/;

export type UpgradeOutcome =
  | "deployed over the older bootstrap"
  | "refused over the older bootstrap, deployed after bootstrapping";

export type UpgradeSteps = {
  deploy: (name: string) => Promise<Ran>;
  bootstrap: () => Promise<Ran>;
  projectExists: () => Promise<boolean>;
};

export function bootstrapBuild(env: NodeJS.ProcessEnv): OcelBuild | undefined {
  const bin = env[BOOTSTRAP_BIN_ENV]?.trim();
  const providersDir = env[BOOTSTRAP_PROVIDERS_DIR_ENV]?.trim();
  if (!bin && !providersDir) {
    return undefined;
  }
  if (!bin || !providersDir) {
    throw new Error(
      `set ${BOOTSTRAP_BIN_ENV} and ${BOOTSTRAP_PROVIDERS_DIR_ENV} together: an older ocel bootstraps only with the providers of its own version`,
    );
  }
  return { bin, providersDir };
}

export function refusedForOlderBootstrap(ran: Ran): boolean {
  if (ran.code === 0) {
    return false;
  }
  const said = stripVTControlCharacters(`${ran.stdout}\n${ran.stderr}`);
  return BEHIND.test(said) && said.includes(BOOTSTRAP_COMMAND);
}

export async function deployOverOlderBootstrap(steps: UpgradeSteps): Promise<UpgradeOutcome> {
  try {
    await steps.deploy("deploy-over-older-bootstrap");
    return "deployed over the older bootstrap";
  } catch (error) {
    if (!(error instanceof NonzeroExitError) || !refusedForOlderBootstrap(error.result)) {
      throw error;
    }
  }
  if (await steps.projectExists()) {
    throw new Error(
      "the deploy over the older bootstrap was refused, yet the project it refused is deployed, so it changed something before refusing",
    );
  }
  await steps.bootstrap();
  await steps.deploy("deploy");
  return "refused over the older bootstrap, deployed after bootstrapping";
}
