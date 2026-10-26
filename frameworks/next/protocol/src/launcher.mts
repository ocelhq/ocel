export const nextIncrementalCacheModule = "next/dist/server/lib/incremental-cache/index.js";

export const nextFileSystemCacheModule =
  "next/dist/server/lib/incremental-cache/file-system-cache.js";

export interface RevalidateWindow {
  revalidate: number;
  expire?: number;
}

export interface DynamicRevalidateWindow {
  routeRegex: string;
  window: RevalidateWindow;
}

export interface LauncherCache {
  fetchCacheKeyPrefix: string;
  isrFlushToDisk?: boolean;
  cacheMaxMemorySize?: number;
  previewModeId?: string;
  routes: Record<string, RevalidateWindow>;
  dynamicRoutes: DynamicRevalidateWindow[];
}

export interface LauncherConfig {
  basePath: string;
  i18n: unknown;
  trailingSlash: boolean;
  experimental: unknown;
  clientAssetSuffix: string;
  distDir: string;
  cache: LauncherCache;
}
