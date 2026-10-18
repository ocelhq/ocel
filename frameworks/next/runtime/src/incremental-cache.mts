import { randomBytes } from "node:crypto";
import type http from "node:http";
import { createRequire } from "node:module";
import { join } from "node:path";
import type { LauncherConfig } from "@framework/next-protocol/launcher";

const incrementalCacheModule = "next/dist/server/lib/incremental-cache/index.js";

interface IncrementalCacheOptions {
  dev: boolean;
  minimalMode: boolean;
  flushToDisk: boolean | undefined;
  serverDistDir: string;
  requestHeaders: http.IncomingHttpHeaders;
  maxMemoryCacheSize: number | undefined;
  fetchCacheKeyPrefix: string;
  getPrerenderManifest: () => unknown;
  CurCacheHandler: new (opts: never) => unknown;
}

type IncrementalCache = new (opts: IncrementalCacheOptions) => unknown;

export type IncrementalCacheFactory = (req: http.IncomingMessage) => unknown;

export function newIncrementalCacheFactory(
  config: LauncherConfig,
  projectDir: string,
  CacheHandler: new (opts: never) => unknown,
): IncrementalCacheFactory {
  const { fetchCacheKeyPrefix, isrFlushToDisk, cacheMaxMemorySize, previewModeId } = config.cache;
  const { IncrementalCache } = createRequire(join(projectDir, "package.json"))(
    incrementalCacheModule,
  ) as { IncrementalCache: IncrementalCache };
  const prerenderManifest = {
    version: 4,
    routes: {},
    dynamicRoutes: {},
    notFoundRoutes: [],
    preview: { previewModeId: previewModeId ?? randomBytes(32).toString("hex") },
  };

  return (req) =>
    new IncrementalCache({
      dev: false,
      minimalMode: false,
      flushToDisk: isrFlushToDisk,
      serverDistDir: join(projectDir, config.distDir, "server"),
      requestHeaders: req.headers,
      maxMemoryCacheSize: cacheMaxMemorySize,
      fetchCacheKeyPrefix,
      getPrerenderManifest: () => prerenderManifest,
      CurCacheHandler: CacheHandler,
    });
}
