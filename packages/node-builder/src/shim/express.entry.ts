import http from "node:http";
import serverless from "serverless-http";

// Replaced at bundle time (esbuild `define`) with a placeholder string that the
// builder substitutes per-app with the transpiled entrypoint's relative path.
declare const __OCEL_ENTRY__: string;

let captured: http.Server | undefined;
const originalListen = http.Server.prototype.listen;
http.Server.prototype.listen = function listen(this: http.Server, ...args: unknown[]) {
  captured = this;
  const cb = args.find((a) => typeof a === "function") as (() => void) | undefined;
  if (cb) process.nextTick(() => cb());
  return this;
} as typeof http.Server.prototype.listen;

// Non-literal specifier keeps esbuild from bundling the user tree at build time:
// the app stays a separate, traced, un-bundled module loaded dynamically here.
const entry = new URL(__OCEL_ENTRY__, import.meta.url);
await import(entry.href);

http.Server.prototype.listen = originalListen;

if (!captured) {
  throw new Error(
    "node-builder: entrypoint imported without starting an HTTP server via listen()",
  );
}

const requestListener = captured.listeners("request")[0];
const wrapped = serverless((requestListener ?? captured) as never);

export const handler = (event: never, context: never) => wrapped(event, context);
