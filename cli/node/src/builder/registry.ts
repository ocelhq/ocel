import { bundlePlan } from "./bundle.js";
import { hasDep } from "./detect.js";
import { buildNext } from "./next.js";
import { traceBuild, type TraceSpec } from "./trace.js";
import type { AppInput, BuildOptions, FunctionSummary } from "./types.js";

export interface Framework {
  name: string;
  detect(dir: string): boolean;
  build(input: AppInput, options: BuildOptions): Promise<FunctionSummary[]>;
}

const RUNTIME = "nodejs24.x";

const NODE_ENTRYPOINTS = [
  "src/server.ts", "src/server.js", "src/index.ts", "src/index.js",
  "src/app.ts", "src/app.js", "index.ts", "index.js",
  "server.ts", "server.js", "app.ts", "app.js",
];

function traced(name: string, dep: string): Framework {
  const spec: TraceSpec = { name, runtime: RUNTIME, entrypointCandidates: NODE_ENTRYPOINTS };
  return {
    name,
    detect: (dir) => hasDep(dir, dep),
    build: async (input, options) => [
      process.env.OCEL_BUILD_PREFER_TRACING === "1"
        ? await traceBuild(input, options, spec)
        : bundlePlan(input, spec),
    ],
  };
}

export const express = traced("express", "express");
export const fastify = traced("fastify", "fastify");
export const hono = traced("hono", "hono");
export const next: Framework = {
  name: "next",
  detect: (dir) => hasDep(dir, "next"),
  build: buildNext,
};

export const REGISTRY: Framework[] = [next, express, fastify, hono];

const byName = new Map(REGISTRY.map((fw) => [fw.name, fw]));

export function resolveFramework(key: string): Framework {
  const fw = byName.get(key);
  if (!fw) {
    throw new Error(`ocel: unknown framework "${key}"; known: ${[...byName.keys()].join(", ")}`);
  }
  return fw;
}

export function detectFramework(dir: string): Framework | undefined {
  return REGISTRY.find((fw) => fw.detect(dir));
}
