import type { TargetName } from "../matrix/types";
import { AwsTarget } from "./aws";
import { DevTarget } from "./dev";
import { DevLocalTarget } from "./devLocal";
import { GcpTarget } from "./gcp";
import type { Target } from "./types";
import { VpsTarget } from "./vps";

export { hasReleaseCycle } from "./types";

const TARGETS: Record<TargetName, () => Target> = {
  aws: () => new AwsTarget(),
  dev: () => new DevTarget(),
  "dev-local": () => new DevLocalTarget(),
  gcp: () => new GcpTarget(),
  vps: () => new VpsTarget(),
};

export function targetNamed(name: string): Target {
  const made = TARGETS[name as TargetName];
  if (!made) {
    const known = Object.keys(TARGETS).join(", ");
    throw new Error(`no journey target named ${name} (${known})`);
  }
  return made();
}

export function laneWorkers(
  target: Pick<Target, "workers">,
  env: NodeJS.ProcessEnv = process.env,
): number {
  const asked = Number((env.OCEL_JOURNEY_WORKERS ?? "").trim());
  return Number.isInteger(asked) && asked > 0 ? asked : target.workers;
}

export function selectedTarget(): Target {
  const name = process.env.OCEL_TARGET;
  if (!name) {
    throw new Error("set OCEL_TARGET to the target this process drives");
  }
  return targetNamed(name);
}
