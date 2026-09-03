import { dev } from "./dev";
import type { ExpectationEnvironment, Expectations } from "./types";

export type { ExpectationEnvironment, Expectations } from "./types";

const files: Record<ExpectationEnvironment, Expectations> = { dev };

export function expectationsFor(environment: ExpectationEnvironment): Expectations {
  const file = files[environment];
  if (!file) {
    throw new Error(`no expectations file for the ${environment} environment`);
  }
  return file;
}

export function expectedIssue(
  expectations: Expectations,
  cell: string,
  title: string,
): string | undefined {
  return expectations[cell]?.[title];
}
