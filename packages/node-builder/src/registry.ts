/**
 * Single registry of framework-specific knowledge: entrypoint candidates,
 * Lambda runtime, and the handler shim. Adding a framework happens here and
 * nowhere else.
 */
export interface Framework {
  name: string;
  runtime: string;
  /** Ordered default entrypoint candidates, relative to the app root. */
  entrypointCandidates: string[];
  /**
   * Generate the `index.mjs` handler shim for a resolved entrypoint.
   * `entryJs` is the transpiled entrypoint path relative to the `.func` dir.
   */
  shim: (entryJs: string) => string;
}

import bundledExpressShim from "./shim/express.bundled";

const ENTRY_PLACEHOLDER = "__OCEL_ENTRY_PLACEHOLDER__";

// The shim is pre-bundled at build time (adapter + serverless-http inlined);
// here we only stamp in the resolved entrypoint path. No bundler at runtime.
const expressShim = (entryJs: string) =>
  bundledExpressShim.replaceAll(ENTRY_PLACEHOLDER, `./${entryJs}`);

export const express: Framework = {
  name: "express",
  runtime: "nodejs24.x",
  entrypointCandidates: [
    "src/server.ts",
    "src/server.js",
    "src/index.ts",
    "src/index.js",
    "src/app.ts",
    "src/app.js",
    "index.ts",
    "index.js",
    "server.ts",
    "server.js",
    "app.ts",
    "app.js",
  ],
  shim: (s) => s,
};

const frameworks: Record<string, Framework> = { express };

export function resolveFramework(key: string | undefined): Framework {
  const fw = frameworks[key ?? "express"];
  if (!fw) {
    throw new Error(
      `node-builder: unknown framework "${key}"; known: ${Object.keys(frameworks).join(", ")}`,
    );
  }
  return fw;
}
