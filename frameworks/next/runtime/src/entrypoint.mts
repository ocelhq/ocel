import type http from "node:http";
import { dirname, isAbsolute, relative } from "node:path";
import { pathToFileURL } from "node:url";
import type { LauncherConfig } from "@framework/next-protocol/launcher";
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
import { beforeWriteHead } from "@framework/node-runtime/write-head";
import {
  cachesOnlyMarked,
  edgeCacheHeader,
  readEdgeCacheMode,
} from "@platform/edge-contract/edge-cache";
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
import { purgedSince, purgeMark } from "./purge-signal.mjs";
import type { RequestHeaders } from "./request-headers.mjs";
import { noteRscRequest } from "./rsc-request.mjs";
import { noteServedRoute, noteUncachedRender } from "./served-route.mjs";
import { loadTagsManifest, mirrorTagsInto } from "./tags-manifest.mjs";

function isPprResume(req: http.IncomingMessage): boolean {
  return req.method === "POST" && req.headers["next-resume"] === "1";
}

function announcePurges(res: http.ServerResponse, release: string): void {
  const before = purgeMark();
  beforeWriteHead(res, (res) => {
    const announced = purgeHeaderFor(release, purgedSince(before));
    if (announced !== undefined) res.setHeader(purgeHeader, announced);
  });
}

async function boot(): Promise<void> {
  const refused = refuseIncompleteHost(getNextHost(), process.env);
  if (refused) throw refused;

  installCompileCacheFlush();
  const CacheHandler = getNextHost().newCacheStore ? installCacheHandlers() : undefined;

  const handlerPath = process.env.OCEL_HANDLER!;
  const href = isAbsolute(handlerPath) ? pathToFileURL(handlerPath).href : handlerPath;
  const relativeProjectDir = relative(process.cwd(), dirname(handlerPath)) || ".";

  await awaitLiveValues();

  const mod: any = (await import(href)).default;
  const handler = mod?.handler;
  if (typeof handler !== "function") {
    throw new Error(`Next launcher ${handlerPath} does not export a handler function`);
  }

  const nextConfig: LauncherConfig | undefined = mod?.nextConfig;
  if (!nextConfig?.cache) {
    throw new Error(`Next launcher ${handlerPath} does not export the config its build wrote`);
  }

  installCompileCacheWarm(mod?.warm);

  if (CacheHandler) mirrorTagsInto(loadTagsManifest(dirname(handlerPath)));

  const newIncrementalCache =
    CacheHandler && newIncrementalCacheFactory(nextConfig, dirname(handlerPath), CacheHandler);
  const routes = revalidatingRoutes(nextConfig);
  const shaping = originShaping(routes, process.env, getNextHost().cacheTagsPerObject);
  const freshOnly = servesOnlyFresh(process.env);
  const edgeCachesOnlyMarked = cachesOnlyMarked(readEdgeCacheMode(process.env));
  const purgeRelease =
    invalidatesByCacheTag(process.env) && edgeCachesOnlyMarked ? releaseOf(process.env) : null;

  const invoke: Invoke = (req, res, ocel) => {
    noteRscRequest(req.headers);
    if (shaping) shapeOriginCache(req, res, shaping);
    if (purgeRelease !== null) announcePurges(res, purgeRelease);
    if (freshOnly) {
      const window = findRouteWindow(req.url, routes);
      if (window) noteServedRoute(req.headers as RequestHeaders, window);
      else if (edgeCachesOnlyMarked && req.headers[edgeCacheHeader] !== "1") {
        noteUncachedRender(req.headers as RequestHeaders);
      }
    }
    if (newIncrementalCache) (globalThis as any).__incrementalCache = newIncrementalCache(req);
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
