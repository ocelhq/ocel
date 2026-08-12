import { functionRel } from "./layout.js";
import { resolveEntrypoint, type TraceSpec } from "./trace.js";
import type { AppInput, FunctionSummary } from "./types.js";

export const BUNDLE_HANDLER = "index.mjs";

export function bundlePlan(input: AppInput, fw: TraceSpec): FunctionSummary {
  return {
    name: input.name,
    runtime: fw.runtime,
    handler: BUNDLE_HANDLER,
    artifactPath: functionRel(input.name),
    framework: fw.name,
    strategy: "bundle",
    entrypoint: resolveEntrypoint(input, fw),
  };
}
