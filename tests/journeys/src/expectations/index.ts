import { aws } from "./aws";
import { awsFloci } from "./aws.floci";
import { dev } from "./dev";
import { EDGE_ENV } from "./edge";
import type { ExpectationEnvironment, Expectations } from "./types";
import { vps } from "./vps";
import { vpsIncus } from "./vps.incus";

export type { ExpectationEnvironment, Expectations } from "./types";

const files: Record<ExpectationEnvironment, () => Expectations> = {
  aws: () => aws(process.env[EDGE_ENV]),
  "aws.floci": () => awsFloci(process.env[EDGE_ENV]),
  dev: () => dev,
  vps: () => vps,
  "vps.incus": () => vpsIncus,
};

export function expectationsFor(environment: ExpectationEnvironment): Expectations {
  const file = files[environment];
  if (!file) {
    throw new Error(`no expectations file for the ${environment} environment`);
  }
  return file();
}
