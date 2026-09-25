import { functionRel } from "./layout.js";
import { functionFramework, resolveEntrypoint } from "./trace.js";
import type { AppInput, FunctionSummary, RuntimeSpec } from "./types.js";

export const BUNDLE_HANDLER = "index.mjs";

export function bundleSummary(input: AppInput, spec: RuntimeSpec): FunctionSummary {
  return {
    name: input.name,
    framework: functionFramework(input, spec),
    handler: BUNDLE_HANDLER,
    artifactPath: functionRel(input.name),
    strategy: "bundle",
    entrypoint: resolveEntrypoint(input, spec),
  };
}
