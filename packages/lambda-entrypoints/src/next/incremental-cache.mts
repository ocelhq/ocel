import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { join } from "node:path";
import type http from "node:http";

// Pages Router bundles never construct Next's incremental cache themselves:
// the App Router templates call routeModule.getIncrementalCache() per request,
// but pages/pages-api code reaches unstable_cache, which resolves
// globalThis.__incrementalCache and throws an invariant when nothing set it.
// `next start`'s base server publishes that global per request; this factory is
// the membrane's equivalent.
//
// It cannot be Next's own IncrementalCache class: the bundle's file trace
// prunes `next` down to what the compiled routes require, and nothing in a
// pages bundle requires `next/dist/server/lib/incremental-cache`. So this is a
// facade over the manifest-named cache handler, providing exactly the surface
// unstable_cache uses — generateSimpleCacheKey, get, set, isOnDemandRevalidate,
// revalidateTag — with Next's FETCH-entry semantics. App Router bundles
// construct their own per request and overwrite the global, so publishing this
// unconditionally is inert for them.

// Next's fetch-cache key version; a mismatch splits the keyspace between pages
// and app routes caching the same unstable_cache call.
const MAIN_KEY_PREFIX = "v4";
const PRERENDER_REVALIDATE_HEADER = "x-prerender-revalidate";

class PagesRuntimeIncrementalCache {
  readonly isOnDemandRevalidate: boolean;
  private readonly handler: any;
  private readonly fetchCacheKeyPrefix: string;

  constructor(opts: {
    handler: any;
    fetchCacheKeyPrefix: string;
    previewModeId: string | undefined;
    requestHeaders: http.IncomingHttpHeaders;
  }) {
    this.handler = opts.handler;
    this.fetchCacheKeyPrefix = opts.fetchCacheKeyPrefix;
    this.isOnDemandRevalidate =
      opts.previewModeId !== undefined &&
      opts.requestHeaders[PRERENDER_REVALIDATE_HEADER] === opts.previewModeId;
  }

  async generateSimpleCacheKey(input: string): Promise<string> {
    const cacheString = JSON.stringify([MAIN_KEY_PREFIX, this.fetchCacheKeyPrefix, input]);
    return createHash("sha256").update(cacheString).digest("hex");
  }

  // FETCH entries only: page/route entries never pass through this object —
  // in the pages runtime they are served from the prerender store at the edge.
  async get(cacheKey: string, ctx: any): Promise<any> {
    if (ctx?.kind !== "FETCH") return null;
    const entry = await this.handler.get(cacheKey, ctx);
    if (!entry || entry.value?.kind !== "FETCH") return null;

    const revalidate = ctx.revalidate || entry.value.revalidate;
    const age = (Date.now() - (entry.lastModified || 0)) / 1000;
    return {
      isStale: age > revalidate,
      value: { kind: "FETCH", data: entry.value.data, revalidate },
    };
  }

  async set(cacheKey: string, data: any, ctx: any): Promise<void> {
    if (data?.kind !== "FETCH") return;
    await this.handler.set(cacheKey, data, ctx);
  }

  async revalidateTag(tags: string | string[], durations?: { expire?: number }): Promise<void> {
    await this.handler.revalidateTag(tags, durations);
  }

  resetRequestCache(): void {
    this.handler.resetRequestCache?.();
  }
}

export type IncrementalCacheFactory = (req: http.IncomingMessage) => unknown;

export async function loadIncrementalCacheFactory(
  projectDir: string,
): Promise<IncrementalCacheFactory | null> {
  let manifest: any;
  try {
    manifest = JSON.parse(
      await readFile(join(projectDir, ".next", "required-server-files.json"), "utf8"),
    );
  } catch {
    return null;
  }
  const config = manifest?.config;
  if (!config?.cacheHandler) return null;

  // The same file the App Router bundles import, required from the app's own
  // tree, so both runtimes share one handler module and its store binding.
  const appRequire = createRequire(join(projectDir, "package.json"));
  const handlerModule = appRequire(config.cacheHandler);
  const CurCacheHandler = handlerModule.default ?? handlerModule;
  const fetchCacheKeyPrefix = config.experimental?.fetchCacheKeyPrefix ?? "";

  const distDir = join(projectDir, config.distDir || ".next");
  let previewModeId: string | undefined;
  try {
    const prerenderManifest = JSON.parse(
      await readFile(join(distDir, "prerender-manifest.json"), "utf8"),
    );
    previewModeId = prerenderManifest?.preview?.previewModeId;
  } catch {}

  return (req) =>
    new PagesRuntimeIncrementalCache({
      // Constructed per request the way RouteModule.getIncrementalCache does,
      // with the arguments Next hands a custom handler.
      handler: new CurCacheHandler({
        dev: false,
        flushToDisk: config.experimental?.isrFlushToDisk,
        serverDistDir: join(distDir, "server"),
        revalidatedTags: [],
        maxMemoryCacheSize: config.cacheMaxMemorySize,
        _requestHeaders: req.headers,
        fetchCacheKeyPrefix,
      }),
      fetchCacheKeyPrefix,
      previewModeId,
      requestHeaders: req.headers,
    });
}
