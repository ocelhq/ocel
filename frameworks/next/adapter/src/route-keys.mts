function routePathname(
  pathname: string,
  basePath: string,
  strip: (pathname: string) => string = unindex,
): string {
  if (!basePath) return strip(pathname);
  if (pathname === basePath) return basePath;
  if (!pathname.startsWith(`${basePath}/`)) return pathname;
  const fixed = strip(pathname.slice(basePath.length));
  return fixed === "/" ? basePath : `${basePath}${fixed}`;
}

const dynamicSegment = /\/\[[^/]+\](?=\/|$)/;

function unindex(pathname: string): string {
  if (dynamicSegment.test(pathname)) return pathname;
  if (pathname === "/index") return "/";
  if (pathname.startsWith("/index/")) return pathname.slice("/index".length);
  return pathname;
}

function unindexLeaf(pathname: string): string {
  if (dynamicSegment.test(pathname)) return pathname;
  if (pathname === "/index") return "/";
  return pathname.endsWith("/index") ? pathname.slice(0, -"/index".length) : pathname;
}

function isPagesRouterKind(type: string): boolean {
  return type === "PAGES" || type === "PAGES_API";
}

// TODO: drop once Next stops classifying by `page.startsWith('/api')` in
// build-complete.ts — that check hands a pages route like /api-docs/[...slug] the
// PAGES_API kind, and an API route never parents a prerender.
export function routeKindsById(
  routes: readonly { id: string; type: string }[],
  prerenders: readonly { parentOutputId: string }[],
): Map<string, string> {
  const prerendered = new Set(prerenders.map((p) => p.parentOutputId));
  return new Map(
    routes.map((r) => [r.id, r.type === "PAGES_API" && prerendered.has(r.id) ? "PAGES" : r.type]),
  );
}

export function isDocumentRouteKind(type: string): boolean {
  return type === "PAGES" || type === "APP_PAGE";
}

export function routeKeyOf(
  output: { pathname: string; type: string; runtime: string },
  basePath: string,
): string {
  if (!isPagesRouterKind(output.type)) return output.pathname;
  return routePathname(
    output.pathname,
    basePath,
    output.runtime === "nodejs" ? unindex : unindexLeaf,
  );
}

export function staticRouteKeyOf(
  file: { pathname: string; filePath: string },
  pagesDistDir: string,
  basePath: string,
): string {
  return file.filePath.startsWith(pagesDistDir)
    ? routePathname(file.pathname, basePath)
    : file.pathname;
}

export function nextDataPathnameOf(pageKey: string, buildId: string, basePath: string): string {
  const unprefixed =
    basePath && pageKey.startsWith(basePath) ? pageKey.slice(basePath.length) || "/" : pageKey;
  const normalized = unprefixed === "/" ? "/index" : unprefixed;
  const dataPathname = `/_next/data/${buildId}${normalized}.json`;
  return basePath ? `${basePath}${dataPathname}` : dataPathname;
}
