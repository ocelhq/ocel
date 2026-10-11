import type http from "node:http";
import { dirname, isAbsolute, relative } from "node:path";
import { pathToFileURL } from "node:url";
import { runWithWaitUntil } from "@framework/node-runtime/background";
import {
  dispatchesAtOrigin,
  type Invoke,
  installCompileCacheFlush,
  installCompileCacheWarm,
  invalidatesByCacheTag,
  reportFatalBoot,
  serveEntry,
  serveInvoke,
  serveLocal,
  servesOnlyFresh,
} from "@framework/node-runtime/host";
import { awaitLiveValues } from "@framework/node-runtime/live-values";
import { purgeHeader, purgeHeaderFor } from "@platform/edge-contract/purge";
import { installCacheHandlers } from "./cache-handlers.mjs";
import {
  findRouteWindow,
  originShaping,
  releaseOf,
  revalidatingRoutes,
  shapeOriginCache,
} from "./cache-shaping.mjs";
import { getNextHost, refuseIncompleteHost } from "./host.mjs";
import { newIncrementalCacheFactory } from "./incremental-cache.mjs";
import { loadProjectManifest } from "./project-manifest.mjs";
import { purgedSince, purgeMark } from "./purge-signal.mjs";
import type { RequestHeaders } from "./request-headers.mjs";
import { noteRscRequest } from "./rsc-request.mjs";
import { noteServedRoute } from "./served-route.mjs";
import { loadTagsManifest, mirrorTagsInto } from "./tags-manifest.mjs";

function isPprResume(req: http.IncomingMessage): boolean {
  return req.method === "POST" && req.headers["next-resume"] === "1";
}

function announcePurges(res: http.ServerResponse, release: string): void {
  const before = purgeMark();
  const writeHead = res.writeHead;
  res.writeHead = function (this: http.ServerResponse, ...args: any[]) {
    if (!this.headersSent) {
      const announced = purgeHeaderFor(release, purgedSince(before));
      if (announced !== undefined) this.setHeader(purgeHeader, announced);
    }
    return (writeHead as any).apply(this, args);
  } as typeof res.writeHead;
}

async function boot(): Promise<void> {
  const refused = refuseIncompleteHost(getNextHost(), process.env);
  if (refused) throw refused;

  installCompileCacheFlush();
  const CacheHandler = installCacheHandlers();

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
  const newIncrementalCache = newIncrementalCacheFactory(manifest, CacheHandler);
  const routes = revalidatingRoutes(manifest);
  const shaping = originShaping(routes, process.env, getNextHost().cacheTagsPerObject);
  const freshOnly = servesOnlyFresh(process.env);
  const purgeRelease =
    invalidatesByCacheTag(process.env) && !dispatchesAtOrigin(process.env)
      ? releaseOf(process.env.OCEL_ISR_PREFIX)
      : null;

  const invoke: Invoke = (req, res, ocel) => {
    noteRscRequest(req.headers);
    if (shaping) shapeOriginCache(req, res, shaping);
    if (purgeRelease !== null) announcePurges(res, purgeRelease);
    if (freshOnly) {
      const window = findRouteWindow(req.url, routes);
      if (window) noteServedRoute(req.headers as RequestHeaders, window);
    }
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

  const { bind, newOriginInvoke } = getNextHost();
  if (!dispatchesAtOrigin(process.env)) {
    await serveInvoke(
      newOriginInvoke ? await newOriginInvoke(invoke) : invoke,
      (port) => {
        process.env.__NEXT_PRIVATE_ORIGIN = `http://127.0.0.1:${port}`;
      },
      bind,
    );
    return;
  }

  const localOrigin = `http://127.0.0.1:${await serveLocal(invoke)}`;
  process.env.__NEXT_PRIVATE_ORIGIN = localOrigin;

  const { newDispatchInvoke } = getNextHost();
  await serveEntry(await newDispatchInvoke!(localOrigin), bind);
}

boot().catch((err) => {
  reportFatalBoot(err);
  process.exit(1);
});
