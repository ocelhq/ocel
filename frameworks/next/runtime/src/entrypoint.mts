import type http from "node:http";
import { dirname, isAbsolute, relative } from "node:path";
import { pathToFileURL } from "node:url";
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
import { originShaping, revalidatingRoutes, shapeOriginCache } from "./cache-shaping.mjs";
import { getNextHost, refuseIncompleteHost } from "./host.mjs";
import { loadIncrementalCacheFactory } from "./incremental-cache.mjs";
import { prerenderedRoutes } from "./prerendered-routes.mjs";
import { loadProjectManifest } from "./project-manifest.mjs";
import { routeStaleHitsToRefresh } from "./refresh.mjs";
import { revalidatedHeader, revalidationTicks } from "./revalidation-signal.mjs";
import { noteRscRequest } from "./rsc-request.mjs";
import { loadTagsManifest, mirrorTagsInto } from "./tags-manifest.mjs";

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
  const refused = refuseIncompleteHost(getNextHost(), process.env);
  if (refused) throw refused;

  installCompileCacheFlush();

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
  const routes = revalidatingRoutes(manifest);
  const prerendered = prerenderedRoutes(manifest);
  const shaping = originShaping(routes, process.env, getNextHost().cacheTagsPerObject);

  const invoke: Invoke = (req, res, ocel) => {
    noteRscRequest(req.headers);
    if (shaping) shapeOriginCache(req, res, shaping);
    announceRevalidations(res);
    const { scheduleRefresh } = getNextHost();
    if (scheduleRefresh)
      routeStaleHitsToRefresh(req, res, prerendered, scheduleRefresh, ocel.holdEnd);
    if (newIncrementalCache) {
      (globalThis as any).__incrementalCache = newIncrementalCache(req);
    }
    return runWithWaitUntil(ocel.holdEnd, () =>
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

  const { newDispatchInvoke } = getNextHost();
  await serveEntry(await newDispatchInvoke!(localOrigin));
}

boot().catch((err) => {
  reportFatalBoot(err);
  process.exit(1);
});
