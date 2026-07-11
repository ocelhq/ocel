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

const expressShim = (entryJs: string) => `import http from "node:http";
import serverless from "serverless-http";

// Intercept listen() so importing the user app captures its server instead of
// binding a port; the request listener on that server is the framework app.
let captured;
const originalListen = http.Server.prototype.listen;
http.Server.prototype.listen = function listen(...args) {
  captured = this;
  const cb = args.find((a) => typeof a === "function");
  if (cb) process.nextTick(() => cb());
  return this;
};

// A non-literal specifier keeps esbuild from bundling the user tree: only the
// adapter shim + its npm deps are inlined; user code stays a separate import.
const entry = new URL(${JSON.stringify("./" + entryJs)}, import.meta.url);
await import(entry.href);

http.Server.prototype.listen = originalListen;

if (!captured) {
  throw new Error(
    "node-builder: entrypoint imported without starting an HTTP server via listen()",
  );
}

const app = captured.listeners("request")[0] ?? captured;
const wrapped = serverless(app);

export const handler = (event, context) => wrapped(event, context);
`;

export const express: Framework = {
  name: "express",
  runtime: "nodejs20.x",
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
  shim: expressShim,
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
