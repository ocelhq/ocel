export interface TrailingSlashConfig {
  basePath: string;
  trailingSlash?: boolean;
  skipTrailingSlashRedirect?: boolean;
  skipMiddlewareUrlNormalize?: boolean;
}

export function withoutBasePath(
  pathname: string,
  basePath: string,
): string | undefined {
  if (!basePath) return pathname;
  if (pathname === basePath) return "";
  if (pathname.startsWith(`${basePath}/`)) return pathname.slice(basePath.length);
  return undefined;
}

const DOTTED_SEGMENT = /\.\w+$/;

function isWellKnown(rest: string): boolean {
  return rest.startsWith("/.well-known");
}

function finalSegment(rest: string): string {
  return rest.split("/").pop() ?? "";
}

function applyPolicy(
  pathname: string,
  config: TrailingSlashConfig,
  isDataRequest: boolean,
): string {
  const basePath = config.basePath ?? "";
  const rest = withoutBasePath(pathname, basePath);
  if (rest === undefined) return pathname;

  if (!config.trailingSlash) {
    const stripped = rest.endsWith("/") ? rest.slice(0, -1) : rest;
    return `${basePath}${stripped}` || "/";
  }

  if (isWellKnown(rest)) return pathname;

  if (rest.endsWith("/")) {
    const dotted = DOTTED_SEGMENT.test(finalSegment(rest.slice(0, -1)));
    if (!dotted || isDataRequest) return pathname;
    return `${basePath}${rest.slice(0, -1)}` || "/";
  }

  if (finalSegment(rest).includes(".")) return pathname;
  return `${basePath}${rest}/`;
}

export function canonicalPathname(
  pathname: string,
  config: TrailingSlashConfig,
  isDataRequest = false,
): string {
  if (config.skipTrailingSlashRedirect) return pathname;
  return applyPolicy(pathname, config, isDataRequest);
}

const REPEATED_SLASH_OR_BACKSLASH = /(\\|\/\/)/;

export function needsSlashNormalization(pathWithoutQuery: string): boolean {
  return REPEATED_SLASH_OR_BACKSLASH.test(pathWithoutQuery);
}

export function normalizeRepeatedSlashes(pathAndQuery: string): string {
  const queryIndex = pathAndQuery.indexOf("?");
  const path = queryIndex === -1 ? pathAndQuery : pathAndQuery.slice(0, queryIndex);
  const query = queryIndex === -1 ? "" : pathAndQuery.slice(queryIndex);
  const normalized = path.replace(/\\/g, "/").replace(/\/\/+/g, "/");
  return normalized + query;
}

export function routingPathname(pathname: string): string {
  if (pathname === "/" || !pathname.endsWith("/")) return pathname;
  return pathname.slice(0, -1);
}

export function dataPagePathname(
  pathname: string,
  config: TrailingSlashConfig,
  buildId: string,
): string | undefined {
  const basePath = config.basePath ?? "";
  const prefix = `${basePath}/_next/data/${buildId}/`;
  if (!pathname.startsWith(prefix) || !pathname.endsWith(".json")) return undefined;
  const page = pathname.slice(prefix.length, -".json".length);
  return `${basePath}${page === "index" ? "" : `/${page}`}` || "/";
}

export function isNextDataPathname(
  pathname: string,
  config: TrailingSlashConfig,
  buildId: string,
): boolean {
  return dataPagePathname(pathname, config, buildId) !== undefined;
}

export function middlewareMatchPathname(
  pathname: string,
  config: TrailingSlashConfig,
  buildId: string,
): string {
  const page = dataPagePathname(pathname, config, buildId);
  if (page !== undefined) {
    const slash = config.trailingSlash && !config.skipMiddlewareUrlNormalize;
    return slash && page !== "/" ? `${page}/` : page;
  }
  return canonicalPathname(pathname, config, false);
}

export function middlewarePathname(
  pathname: string,
  config: TrailingSlashConfig,
  buildId: string,
): string {
  if (config.skipMiddlewareUrlNormalize) return pathname;
  return middlewareMatchPathname(pathname, config, buildId);
}
