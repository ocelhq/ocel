import { posix } from "node:path";

export function cacheKey(key: string): string {
  return key === "/" || key === "" ? "index" : key.replace(/^\//, "");
}

export const variantHeadersFile = "variant-headers.json";

export const cacheHandlerFile = "cache-handler.cjs";
export const useCacheDefaultFile = "use-cache-default.cjs";
export const useCacheRemoteFile = "use-cache-remote.cjs";

export function addCacheHandlers(
  config: { cacheHandlers?: Record<string, string> },
  runtimeDir: string,
): { cacheHandler: string; cacheHandlers: Record<string, string> } {
  return {
    cacheHandler: posix.join(runtimeDir, cacheHandlerFile),
    cacheHandlers: {
      ...config.cacheHandlers,
      default: posix.join(runtimeDir, useCacheDefaultFile),
      remote: posix.join(runtimeDir, useCacheRemoteFile),
    },
  };
}
