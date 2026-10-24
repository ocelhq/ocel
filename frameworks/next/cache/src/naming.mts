export function cacheKey(key: string): string {
  return key === "/" || key === "" ? "index" : key.replace(/^\//, "");
}

const dynamicSegment = /\/\[[^/]+\](?=\/|$)/;

const indexPrefix = /^\/index(\/|$)/;

function pagePathOf(route: string): string {
  if (route === "/" || route === "") return "/index";
  return indexPrefix.test(route) && !dynamicSegment.test(route) ? `/index${route}` : route;
}

export function routeCacheKey(route: string): string {
  return cacheKey(pagePathOf(route));
}

export function routeOf(pagePath: string): string {
  if (pagePath === "/index") return "/";
  return pagePath.startsWith("/index/") && !dynamicSegment.test(pagePath)
    ? pagePath.slice("/index".length)
    : pagePath;
}

export const variantHeadersFile = "variant-headers.json";
