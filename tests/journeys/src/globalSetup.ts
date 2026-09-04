import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import type { TestProject } from "vitest/node";
import { currentRunIdentity } from "./identity";
import { prepareFile } from "./paths";
import { selectedTarget } from "./targets";

export const PREPARE_FAILURE = "journeyPrepareFailure";

declare module "vitest" {
  interface ProvidedContext {
    journeyPrepareFailure: string | undefined;
  }
}

export default async function prepareLane(project: TestProject): Promise<void> {
  const target = selectedTarget();
  const began = Date.now();
  try {
    await target.prepare?.();
    project.provide(PREPARE_FAILURE, undefined);
  } catch (error) {
    project.provide(PREPARE_FAILURE, error instanceof Error ? error.message : String(error));
  }
  const file = prepareFile(currentRunIdentity(), target.name);
  await mkdir(path.dirname(file), { recursive: true });
  await writeFile(file, `${JSON.stringify({ ms: Date.now() - began })}\n`, "utf8");
}
