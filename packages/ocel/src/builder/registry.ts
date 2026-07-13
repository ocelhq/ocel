/**
 * Single registry of framework-specific knowledge: entrypoint candidates and
 * the Lambda runtime. Adding a framework happens here and nowhere else.
 *
 * There is no handler shim: the lambdanode entrypoint imports the user's transpiled
 * entrypoint directly (see build.ts), so the framework only needs to describe
 * where that entrypoint lives and which runtime to run it on.
 */
export interface Framework {
  name: string;
  runtime: string;
  /** Ordered default entrypoint candidates, relative to the app root. */
  entrypointCandidates: string[];
}

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
};

export const fastify: Framework = {
  name: "fastify",
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
};

const frameworks: Record<string, Framework> = { express, fastify };

export function resolveFramework(key: string | undefined): Framework {
  const fw = frameworks[key ?? "express"];
  if (!fw) {
    throw new Error(
      `ocel: unknown framework "${key}"; known: ${Object.keys(frameworks).join(", ")}`,
    );
  }
  return fw;
}
