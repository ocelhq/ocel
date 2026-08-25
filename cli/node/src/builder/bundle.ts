import { functionRel } from "./layout.js";
import { resolveEntrypoint } from "./trace.js";
import type { AppInput, FrameworkSpec, FunctionSummary } from "./types.js";

export const BUNDLE_HANDLER = "index.mjs";

export function bundleSummary(input: AppInput, spec: FrameworkSpec): FunctionSummary {
  return {
    name: input.name,
    runtime: spec.runtime,
    handler: BUNDLE_HANDLER,
    artifactPath: functionRel(input.name),
    framework: spec.name,
    strategy: "bundle",
    entrypoint: resolveEntrypoint(input, spec),
  };
}
