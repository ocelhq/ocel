import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import type { TestProject } from "vitest/node";
import { currentRunIdentity } from "./identity";
import { prepareFile } from "./paths";
import { PREPARE_FAILURE, type PrepareFailures } from "./prepare";
import { selectedTarget } from "./targets";

export default async function prepareLane(project: TestProject): Promise<void> {
  const target = selectedTarget();
  const began = Date.now();
  let failures: PrepareFailures = {};
  try {
    failures = (await target.prepare?.()) ?? {};
  } catch (error) {
    failures = { lane: error instanceof Error ? error.message : String(error) };
  }
  project.provide(PREPARE_FAILURE, failures);
  const file = prepareFile(currentRunIdentity(), target.name);
  await mkdir(path.dirname(file), { recursive: true });
  await writeFile(file, `${JSON.stringify({ ms: Date.now() - began })}\n`, "utf8");
}
