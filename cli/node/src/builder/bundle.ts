import { functionRel } from "./layout.js";
import { functionRuntime, resolveEntrypoint } from "./trace.js";
import type { AppInput, FunctionSummary, RuntimeSpec } from "./types.js";

export const BUNDLE_HANDLER = "index.mjs";

export function bundleSummary(input: AppInput, spec: RuntimeSpec): FunctionSummary {
  return {
    name: input.name,
    runtime: functionRuntime(input, spec),
    handler: BUNDLE_HANDLER,
    artifactPath: functionRel(input.name),
    strategy: "bundle",
    entrypoint: resolveEntrypoint(input, spec),
  };
}
