import { aws } from "./aws";
import { awsFloci } from "./aws.floci";
import { dev } from "./dev";
import type { ExpectationEnvironment, Expectations } from "./types";

export type { ExpectationEnvironment, Expectations } from "./types";

const files: Record<ExpectationEnvironment, Expectations> = {
  aws,
  "aws.floci": awsFloci,
  dev,
};

export function expectationsFor(environment: ExpectationEnvironment): Expectations {
  const file = files[environment];
  if (!file) {
    throw new Error(`no expectations file for the ${environment} environment`);
  }
  return file;
}
