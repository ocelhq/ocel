import { dirname, isAbsolute, relative } from "node:path";
import { pathToFileURL } from "node:url";
import { runWithWaitUntil } from "../shared/background.mjs";
import { loadIncrementalCacheFactory } from "./incremental-cache.mjs";
import { awaitLiveValues } from "../shared/live-values.mjs";
import {
  installCompileCacheFlush,
  installCompileCacheWarm,
  reportFatalBoot,
  serveInvoke,
  type Invoke,
} from "../shared/membrane.mjs";

// Next deletes the flight headers off the live request object before it
// constructs the incremental cache for a prerendered route, so by the time the
// cache handler sees those same headers it can no longer tell an RSC request
// from a document one — and would answer both with the html variant. Marking
// here, before Next runs, is the only point where the distinction still exists.
// The key is a symbol so it stays out of `Object.keys`, and therefore out of
// everything that enumerates or serializes headers, including the app-visible
// `headers()`. Registered rather than local because the cache handler is a
// separate bundle: only the global registry makes the two the same symbol.
const RSC_REQUEST = Symbol.for("ocel.rsc-request");

async function boot(): Promise<void> {
  installCompileCacheFlush();

  // OCEL_HANDLER points at the generated launcher beside the app's .next dir,
  // so its dirname is the Next project root and its default export is the
  // compiled route module.
  const handlerPath = process.env.OCEL_HANDLER!;
  const href = isAbsolute(handlerPath) ? pathToFileURL(handlerPath).href : handlerPath;
  const relativeProjectDir = relative(process.cwd(), dirname(handlerPath)) || ".";

  // The app's module scope runs inside the import below, and a live value read
  // there must already be in hand. A function that declares none never waits.
  await awaitLiveValues();

  const mod: any = (await import(href)).default;
  const handler = mod?.handler;
  if (typeof handler !== "function") {
    throw new Error(`Next launcher ${handlerPath} does not export a handler function`);
  }

  installCompileCacheWarm(mod?.warm);

  const newIncrementalCache = await loadIncrementalCacheFactory(dirname(handlerPath));

  // Next's cache handlers are loaded as their own module graphs and cannot see
  // this context, so the invocation's waitUntil is also published through the
  // background bridge — which is how a handler defers work onto the request it
  // is serving without the request waiting for it.
  const invoke: Invoke = (req, res, ocel) => {
    if (req.headers.rsc === "1") (req.headers as any)[RSC_REQUEST] = true;
    // Pages Router bundles read unstable_cache's incremental cache off
    // globalThis (see incremental-cache.mts); App Router bundles construct
    // their own per request and overwrite this, so publishing it
    // unconditionally is inert for them.
    if (newIncrementalCache) {
      (globalThis as any).__incrementalCache = newIncrementalCache(req);
    }
    return runWithWaitUntil(ocel.waitUntil, () =>
      handler(req, res, {
        waitUntil: ocel.waitUntil,
        requestMeta: { relativeProjectDir, hostname: req.headers.host },
      }),
    );
  };

  // Next forwards a Server Action to itself — to another route, or to follow an
  // app-relative redirect — with a plain fetch, and derives that fetch's origin
  // from the request's Host, which is the public one. Left alone the call leaves
  // the sandbox and comes back through the edge, and the outer streamed response
  // does not survive the round trip. `next start` pins the same fetch to the
  // address it bound; this is that, for the address the membrane bound.
  await serveInvoke(invoke, (port) => {
    process.env.__NEXT_PRIVATE_ORIGIN = `http://127.0.0.1:${port}`;
  });
}

boot().catch((err) => {
  reportFatalBoot(err);
  process.exit(1);
});
