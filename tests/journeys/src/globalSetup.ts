import type { TestProject } from "vitest/node";
import { selectedTarget } from "./targets";

export const PREPARE_FAILURE = "journeyPrepareFailure";

declare module "vitest" {
  interface ProvidedContext {
    journeyPrepareFailure: string | undefined;
  }
}

export default async function prepareLane(project: TestProject): Promise<void> {
  try {
    await selectedTarget().prepare?.();
    project.provide(PREPARE_FAILURE, undefined);
  } catch (error) {
    project.provide(PREPARE_FAILURE, error instanceof Error ? error.message : String(error));
  }
}
