import { readdirSync, lstatSync } from "node:fs";
import http from "node:http";
import Module from "node:module";
import { isAbsolute, join } from "node:path";
import { pathToFileURL } from "node:url";
import { awaitLiveValues } from "../shared/live-values.mjs";
import { fetchToNodeHandler, type FetchHandler } from "./fetch-bridge.mjs";
import {
  installCompileCacheFlush,
  installCompileCacheWarm,
  reportFatalBoot,
  serveInvoke,
  startServer,
  UNSUPPORTED_WARM,
  type Invoke,
  type WarmReport,
} from "../shared/membrane.mjs";

type Loaded =
  | { kind: "server"; value: http.Server }
  | { kind: "export"; value: unknown };

async function loadUserApp(entrypoint: string): Promise<Loaded> {
  const href = isAbsolute(entrypoint) ? pathToFileURL(entrypoint).href : entrypoint;

  const listenHook = interceptListen();

  const importPromise: Promise<Loaded> = import(href).then((mod) => {
    let exported: any = mod;
    for (let i = 0; i < 5; i++) {
      if (exported.default) exported = exported.default;
    }
    return { kind: "export", value: exported };
  });

  const serverPromise: Promise<Loaded> = listenHook
    .waitForServer()
    .then((server) => ({ kind: "server", value: server }));

  const result = await Promise.race([
    serverPromise,
    importPromise.then((r) => {
      // Prefer the export if it's itself a server/app; otherwise keep waiting
      // for a .listen() capture (Nest resolves via serverPromise).
      const v = r.value as any;
      if (v && (typeof v === "function" || typeof v.listen === "function")) {
        return r;
      }
      return serverPromise;
    }),
  ]);

  listenHook.restore();
  return result;
}

type NodeHandler = (req: http.IncomingMessage, res: http.ServerResponse) => void;

type Resolved =
  | { type: "server"; server: http.Server }
  | { type: "node-handler"; handler: NodeHandler }
  | { type: "web-handler"; fetch: FetchHandler };

function resolveHandler(exported: any): Resolved {
  // Callability MUST be checked before `.listen`: an Express `app` is both a
  // function and has `.listen`, so a `.listen`-first check would route it to
  // the "server" branch, and app.address() (nonexistent) would later throw.
  // Treating it as a node-handler makes us wrap it in http.createServer.
  if (typeof exported === "function") {
    return { type: "node-handler", handler: exported };
  }
  if (exported && typeof exported.listen === "function") {
    return { type: "server", server: exported };
  }
  if (exported && typeof exported.fetch === "function") {
    return { type: "web-handler", fetch: exported.fetch };
  }
  const methods = ["GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"];
  if (exported && methods.some((m) => typeof exported[m] === "function")) {
    return { type: "web-handler", fetch: dispatchByMethod(exported) };
  }
  throw new Error(
    "Default export must be an Express app, a (req,res) handler, or a fetch handler.",
  );
}

function invokeFor(resolved: Resolved): Invoke {
  if (resolved.type === "node-handler") return resolved.handler;
  if (resolved.type === "web-handler") return fetchToNodeHandler(resolved.fetch);
  throw new Error(`cannot build an invoke for resolved type: ${resolved.type}`);
}

function dispatchByMethod(exported: any): FetchHandler {
  return (request) => {
    const fn = exported[request.method];
    if (typeof fn !== "function") return new Response(null, { status: 405 });
    return fn(request);
  };
}

interface ListenHook {
  waitForServer: () => Promise<http.Server>;
  restore: () => void;
}

function interceptListen(): ListenHook {
  const realListen = http.Server.prototype.listen;
  let captured: http.Server | null = null;
  const waiters: Array<(server: http.Server) => void> = [];

  http.Server.prototype.listen = function (this: http.Server, ...args: any[]) {
    // Restore immediately so our own later listen() binds for real; the user's
    // .listen() is captured but never actually binds their port.
    http.Server.prototype.listen = realListen;
    captured = this;
    const cb = args.find((a) => typeof a === "function");
    if (cb) setImmediate(cb);
    waiters.forEach((w) => w(this));
    return this;
  } as typeof http.Server.prototype.listen;

  return {
    waitForServer: () =>
      new Promise((resolve) => {
        if (captured) resolve(captured);
        else waiters.push(resolve);
      }),
    restore: () => {
      http.Server.prototype.listen = realListen;
    },
  };
}

// Sums the on-disk size of a flushed compile cache directory, the same way
// next-dispatch.cjs's measureCompileCache does for entry-table bundles — a
// missing directory measures as zero (nothing has been compiled yet), any
// other read failure as null so the caller can tell "empty" from "unknown"
// rather than reporting a partial total as if it were the whole one.
function measureCompileCacheDir(dir: string): number | null {
  let items;
  try {
    items = readdirSync(dir, { withFileTypes: true, recursive: true });
  } catch (err: any) {
    return err?.code === "ENOENT" ? 0 : null;
  }
  let total = 0;
  for (const item of items) {
    if (!item.isFile()) continue;
    try {
      total += lstatSync(join((item as any).parentPath, item.name)).size;
    } catch (err: any) {
      if (err?.code !== "ENOENT") return null;
    }
  }
  return total;
}

// A node app has no entry table to walk — loadUserApp imports the whole
// application before server-ready ever fires, so by the time a warm
// invocation can reach this handler the module graph is already as loaded as
// it will get (boot() would have died on a broken import, and this handler
// would never have been installed to reply at all). There is nothing left to
// warm, so this reports what that already-finished load put in the compile
// cache — the measured bytes and dir — rather than fabricating an
// entry-table walk that never happened: entries/loaded/failures/stoppedBy
// describe a walk this launcher never took, and claiming entries:1, loaded:1
// for "the app itself" as a stand-in unit is exactly that fabrication, just
// dressed as a plausible-looking number instead of an obviously fake one. A
// caller reading the counts alone could not tell that report apart from a
// Next bundle that really did walk one route and stop.
//
// state: "loaded-at-init" is what tells the difference honestly. It says
// positively that the whole graph is already loaded — no entry table exists
// to walk, not merely that this one wasn't — which is a stronger and
// different claim than "unsupported" (no compile-cache API at all, e.g. an
// old node) or "warmed" (a walk ran and these are its real counts).
function warmNode(): WarmReport {
  const { getCompileCacheDir, flushCompileCache } = Module;
  if (typeof getCompileCacheDir !== "function" || typeof flushCompileCache !== "function") {
    return UNSUPPORTED_WARM;
  }
  const dir = getCompileCacheDir();
  if (typeof dir !== "string" || dir.length === 0) return UNSUPPORTED_WARM;
  try {
    flushCompileCache();
  } catch {
    return UNSUPPORTED_WARM;
  }
  const bytes = measureCompileCacheDir(dir);
  if (bytes === null) return UNSUPPORTED_WARM;
  return {
    ok: true,
    state: "loaded-at-init",
    entries: 0,
    loaded: 0,
    failures: [],
    stoppedBy: "complete",
    skipped: [],
    skippedCount: 0,
    bytes,
    dir,
  };
}

async function boot(): Promise<void> {
  installCompileCacheFlush();
  installCompileCacheWarm(warmNode);

  // The user's module scope runs inside the import below, and a live value read
  // there must already be in hand. The membrane's fetch overlaps this process
  // starting, so the wait is usually already over; a function that declares no
  // live value never waits at all.
  await awaitLiveValues();
  const loaded = await loadUserApp(process.env.OCEL_HANDLER!);
  if (loaded.kind === "server") {
    await startServer(loaded.value);
  } else {
    await serveInvoke(invokeFor(resolveHandler(loaded.value)));
  }
}

boot().catch((err) => {
  reportFatalBoot(err);
  process.exit(1);
});
