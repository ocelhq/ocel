import type http from "node:http";
import { dirname, isAbsolute, relative } from "node:path";
import { pathToFileURL } from "node:url";
import { originShaping, shapeOriginCache } from "@framework/next-runtime/cache-shaping";
import { installNextHost } from "@framework/next-runtime/host";
import { loadIncrementalCacheFactory } from "@framework/next-runtime/incremental-cache";
import { loadProjectManifest } from "@framework/next-runtime/project-manifest";
import { revalidatedHeader, revalidationTicks } from "@framework/next-runtime/revalidation-signal";
import { loadTagsManifest, mirrorTagsInto } from "@framework/next-runtime/tags-manifest";
import { runWithWaitUntil } from "@framework/node-runtime/background";
import {
  dispatchesAtOrigin,
  type Invoke,
  installCompileCacheFlush,
  installCompileCacheWarm,
  reportFatalBoot,
  serveEntry,
  serveInvoke,
  serveLocal,
} from "@framework/node-runtime/host";
import { awaitLiveValues } from "@framework/node-runtime/live-values";

const RSC_REQUEST = Symbol.for("ocel.rsc-request");

function isPprResume(req: http.IncomingMessage): boolean {
  return req.method === "POST" && req.headers["next-resume"] === "1";
}

function announceRevalidations(res: http.ServerResponse): void {
  const before = revalidationTicks();
  const writeHead = res.writeHead;
  res.writeHead = function (this: http.ServerResponse, ...args: any[]) {
    if (!this.headersSent && revalidationTicks() !== before) {
      this.setHeader(revalidatedHeader, "1");
    }
    return (writeHead as any).apply(this, args);
  } as typeof res.writeHead;
}

async function boot(): Promise<void> {
  installCompileCacheFlush();
  installNextHost({
    newCacheStore: async () => (await import("./cache-store.mjs")).awsCacheStore(),
    newUseCacheStore: async () => (await import("./use-cache-store.mjs")).awsUseCacheStore(),
  });

  const handlerPath = process.env.OCEL_HANDLER!;
  const href = isAbsolute(handlerPath) ? pathToFileURL(handlerPath).href : handlerPath;
  const relativeProjectDir = relative(process.cwd(), dirname(handlerPath)) || ".";

  await awaitLiveValues();

  const mod: any = (await import(href)).default;
  const handler = mod?.handler;
  if (typeof handler !== "function") {
    throw new Error(`Next launcher ${handlerPath} does not export a handler function`);
  }

  installCompileCacheWarm(mod?.warm);

  mirrorTagsInto(loadTagsManifest(dirname(handlerPath)));

  const manifest = await loadProjectManifest(dirname(handlerPath));
  const newIncrementalCache = loadIncrementalCacheFactory(dirname(handlerPath), manifest);
  const shaping = originShaping(manifest, process.env);

  const invoke: Invoke = (req, res, ocel) => {
    if (req.headers.rsc === "1") (req.headers as any)[RSC_REQUEST] = true;
    if (shaping) shapeOriginCache(req, res, shaping);
    announceRevalidations(res);
    if (newIncrementalCache) {
      (globalThis as any).__incrementalCache = newIncrementalCache(req);
    }
    return runWithWaitUntil(ocel.waitUntil, () =>
      handler(req, res, {
        waitUntil: ocel.waitUntil,
        requestMeta: {
          relativeProjectDir,
          hostname: req.headers.host,
          ...(isPprResume(req) && { minimalMode: true }),
        },
      }),
    );
  };

  if (!dispatchesAtOrigin(process.env)) {
    await serveInvoke(invoke, (port) => {
      process.env.__NEXT_PRIVATE_ORIGIN = `http://127.0.0.1:${port}`;
    });
    return;
  }

  const localOrigin = `http://127.0.0.1:${await serveLocal(invoke)}`;
  process.env.__NEXT_PRIVATE_ORIGIN = localOrigin;

  const { readDispatchHost, newDispatchInvoke } = await import("./dispatch-host.mjs");
  await serveEntry(newDispatchInvoke(readDispatchHost(process.env, localOrigin)));
}

boot().catch((err) => {
  reportFatalBoot(err);
  process.exit(1);
});
