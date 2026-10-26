export function cacheKey(key: string): string {
  return key === "/" || key === "" ? "index" : key.replace(/^\//, "");
}

export function pageCacheKey(pathname: string): string {
  const isDynamic = /\[[^/]+\]/.test(pathname);
  return cacheKey(/^\/index(\/|$)/.test(pathname) && !isDynamic ? `/index${pathname}` : pathname);
}

export const variantHeadersFile = "variant-headers.json";
