import { bundleSummary } from "./bundle.js";
import { hasDep, hasPackageJson } from "./detect.js";
import { buildNext } from "./next.js";
import { traceBuild } from "./trace.js";
import type { AppInput, BuildOptions, FunctionSummary, RuntimeSpec } from "./types.js";

export interface Runtime {
  name: string;
  detect(dir: string): boolean;
  build(input: AppInput, options: BuildOptions): Promise<FunctionSummary[]>;
}

const NODE_ENTRYPOINTS = [
  "src/server.ts", "src/server.js", "src/index.ts", "src/index.js",
  "src/app.ts", "src/app.js", "index.ts", "index.js",
  "server.ts", "server.js", "app.ts", "app.js",
];

const nodeSpec: RuntimeSpec = { name: "node", entrypointCandidates: NODE_ENTRYPOINTS };

export const node: Runtime = {
  name: "node",
  detect: hasPackageJson,
  build: async (input, options) => [
    process.env.OCEL_BUILD_PREFER_TRACING === "1"
      ? await traceBuild(input, options, nodeSpec)
      : bundleSummary(input, nodeSpec),
  ],
};

export const next: Runtime = {
  name: "next",
  detect: (dir) => hasDep(dir, "next"),
  build: buildNext,
};

export const REGISTRY: Runtime[] = [next, node];

const byName = new Map(REGISTRY.map((rt) => [rt.name, rt]));

export function resolveRuntime(key: string): Runtime {
  const rt = byName.get(key);
  if (!rt) {
    throw new Error(`ocel: unknown runtime "${key}"; known: ${[...byName.keys()].join(", ")}`);
  }
  return rt;
}

export function detectRuntime(dir: string): Runtime | undefined {
  return REGISTRY.find((rt) => rt.detect(dir));
}
