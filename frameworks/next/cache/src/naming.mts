export function cacheKey(key: string): string {
  return key === "/" || key === "" ? "index" : key.replace(/^\//, "");
}

export const variantHeadersFile = "variant-headers.json";

export const cacheHandlerFile = "cache-handler.cjs";
export const useCacheDefaultFile = "use-cache-default.cjs";
export const useCacheRemoteFile = "use-cache-remote.cjs";
