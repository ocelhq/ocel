import { createHash } from "node:crypto";
import { createRequire } from "node:module";
import { join } from "node:path";
import type http from "node:http";
import type { ProjectManifest } from "./project-manifest.mjs";

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

export function loadIncrementalCacheFactory(
  projectDir: string,
  manifest: ProjectManifest | null,
): IncrementalCacheFactory | null {
  if (!manifest?.config?.cacheHandler) return null;
  const { config, distDir } = manifest;

  const appRequire = createRequire(join(projectDir, "package.json"));
  const handlerModule = appRequire(config.cacheHandler);
  const CurCacheHandler = handlerModule.default ?? handlerModule;
  const fetchCacheKeyPrefix = config.experimental?.fetchCacheKeyPrefix ?? "";
  const previewModeId: string | undefined = manifest.prerender?.preview?.previewModeId;

  return (req) =>
    new PagesRuntimeIncrementalCache({
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
