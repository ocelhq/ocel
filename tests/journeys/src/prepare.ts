import type { Edge } from "./spec";

export const PREPARE_FAILURE = "journeyPrepareFailure";

export type PrepareFailures = {
  lane?: string;
  edges?: { [K in Edge]?: string };
};

declare module "vitest" {
  interface ProvidedContext {
    journeyPrepareFailure: PrepareFailures;
  }
}

export function failureFor(
  failures: PrepareFailures,
  edge: Edge | undefined,
): string | undefined {
  if (failures.lane) {
    return failures.lane;
  }
  return edge === undefined ? undefined : failures.edges?.[edge];
}
