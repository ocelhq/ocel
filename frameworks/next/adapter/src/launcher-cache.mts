import { readFile } from "node:fs/promises";
import { join } from "node:path";
import type {
  DynamicRevalidateWindow,
  LauncherCache,
  RevalidateWindow,
} from "@framework/next-protocol/launcher";

interface PrerenderManifest {
  routes?: Record<string, { initialRevalidateSeconds?: unknown; initialExpireSeconds?: unknown }>;
  dynamicRoutes?: Record<
    string,
    { routeRegex?: unknown; fallbackRevalidate?: unknown; fallbackExpire?: unknown }
  >;
  preview?: { previewModeId?: unknown };
}

export interface CacheConfig {
  cacheMaxMemorySize?: number;
  experimental?: { fetchCacheKeyPrefix?: string; isrFlushToDisk?: boolean };
}

function windowOf(revalidate: unknown, expire: unknown): RevalidateWindow | null {
  if (typeof revalidate !== "number" || revalidate <= 0) return null;
  return {
    revalidate,
    ...(typeof expire === "number" && expire > revalidate && { expire }),
  };
}

async function readPrerenderManifest(distDir: string): Promise<PrerenderManifest> {
  const path = join(distDir, "prerender-manifest.json");
  try {
    return JSON.parse(await readFile(path, "utf8")) as PrerenderManifest;
  } catch (error) {
    throw new Error(`ocel: next build left no readable ${path}`, { cause: error });
  }
}

export async function readLauncherCache(
  distDir: string,
  config: CacheConfig,
): Promise<LauncherCache> {
  const manifest = await readPrerenderManifest(distDir);

  const routes: Record<string, RevalidateWindow> = {};
  for (const [path, entry] of Object.entries(manifest.routes ?? {})) {
    const window = windowOf(entry?.initialRevalidateSeconds, entry?.initialExpireSeconds);
    if (window) routes[path] = window;
  }

  const dynamicRoutes: DynamicRevalidateWindow[] = [];
  for (const entry of Object.values(manifest.dynamicRoutes ?? {})) {
    if (typeof entry?.routeRegex !== "string") continue;
    const window = windowOf(entry.fallbackRevalidate, entry.fallbackExpire);
    if (window) dynamicRoutes.push({ routeRegex: entry.routeRegex, window });
  }

  const previewModeId = manifest.preview?.previewModeId;
  return {
    fetchCacheKeyPrefix: config.experimental?.fetchCacheKeyPrefix ?? "",
    ...(config.experimental?.isrFlushToDisk !== undefined && {
      isrFlushToDisk: config.experimental.isrFlushToDisk,
    }),
    ...(config.cacheMaxMemorySize !== undefined && {
      cacheMaxMemorySize: config.cacheMaxMemorySize,
    }),
    ...(typeof previewModeId === "string" && { previewModeId }),
    routes,
    dynamicRoutes,
  };
}
