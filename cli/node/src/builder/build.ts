import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { BUILD_PLAN_FILE, sanitizeName } from "./layout.js";
import { withSpan } from "./protocol.js";
import { detectRuntime, resolveRuntime } from "./registry.js";
import type { AppInput, BuildOptions, FunctionSummary } from "./types.js";

export { placeFile } from "./trace.js";
export type { Placement } from "./trace.js";

export async function buildApp(input: AppInput, options: BuildOptions): Promise<FunctionSummary[]> {
  const rt = input.runtime?.name ? resolveRuntime(input.runtime.name) : detectRuntime(input.cwd);
  if (!rt) {
    throw new Error(`ocel: could not detect a runtime in ${input.cwd}; set "runtime" in the app config`);
  }
  return rt.build(input, options);
}

export async function buildApps(inputs: AppInput[], options: BuildOptions): Promise<FunctionSummary[]> {
  const summaries: FunctionSummary[] = [];
  for (const input of inputs) {
    summaries.push(...(await withSpan("build", input.name, () => buildApp(input, options))));
  }
  return summaries;
}

export async function writeBuildPlan(
  outDir: string,
  functions: FunctionSummary[],
): Promise<void> {
  await mkdir(outDir, { recursive: true });
  await writeFile(
    path.join(outDir, BUILD_PLAN_FILE),
    `${JSON.stringify({ functions }, null, 2)}\n`,
  );
}

export function detectApp(projectRoot: string): AppInput | undefined {
  const rt = detectRuntime(projectRoot);
  if (!rt) return undefined;
  return { name: sanitizeName(path.basename(projectRoot)) || "app", cwd: projectRoot, runtime: { name: rt.name } };
}
