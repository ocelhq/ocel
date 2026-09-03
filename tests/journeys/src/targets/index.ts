import type { TargetName } from "../spec";
import { awsTarget } from "./aws";
import { devTarget } from "./dev";
import type { Target } from "./types";
import { vpsTarget } from "./vps";

export type { CellContext, Deployment, Target } from "./types";

const targets: Partial<Record<TargetName, Target>> = {
  aws: awsTarget,
  dev: devTarget,
  vps: vpsTarget,
};

export function targetNamed(name: string): Target {
  const target = targets[name as TargetName];
  if (!target) {
    const known = Object.keys(targets).join(", ");
    throw new Error(`no journey target named ${name} (${known})`);
  }
  return target;
}

export function selectedTarget(): Target {
  const name = process.env.OCEL_TARGET;
  if (!name) {
    throw new Error("set OCEL_TARGET to the target this process drives");
  }
  return targetNamed(name);
}
