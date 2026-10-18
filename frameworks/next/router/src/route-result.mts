import type { NextRouteTable, RoutingTable } from "@framework/next-protocol/route-table";

export interface MiddlewareOutcome {
  response: Response;
  headers: Headers;
}

export interface RouteResult {
  middlewareResponded?: boolean;
  status?: number;
  redirect?: { url: URL | string; status: number };
  externalRewrite?: string | URL;
  resolvedPathname?: string | null;
  routePath?: string | null;
  invocationTarget?: {
    pathname: string;
    query?: Record<string, string | string[]>;
  } | null;
  resolvedQuery?: Record<string, string | string[]>;
  routeMatches?: Record<string, string | string[]>;
  resolvedHeaders?: Headers;
  middleware?: MiddlewareOutcome;
  middlewareLocation?: string;
}

export function repairPrefixCollidedQuery(result: RouteResult): void {
  const routeMatches = result.routeMatches;
  if (!routeMatches) return;
  const query = result.invocationTarget?.query;
  const resolvedQuery = result.resolvedQuery;
  for (const [key, value] of Object.entries(routeMatches)) {
    if (value === undefined) continue;
    if (/^\d+$/.test(key)) continue;
    if (query && key in query) query[key] = value;
    if (resolvedQuery && key in resolvedQuery) resolvedQuery[key] = value;
  }
}

function decodedPathname(pathname: string): string | undefined {
  try {
    return decodeURIComponent(pathname);
  } catch {
    return undefined;
  }
}

export function preferExactPathname(result: RouteResult, manifest: NextRouteTable): RouteResult {
  const target = result.invocationTarget?.pathname;
  if (!result.resolvedPathname || target === undefined) return result;
  for (const candidate of [target, decodedPathname(target)]) {
    if (
      candidate !== undefined &&
      candidate !== result.resolvedPathname &&
      manifest.pathnames.includes(candidate)
    ) {
      return { ...result, resolvedPathname: candidate };
    }
  }
  return result;
}

export function dropShadowedDynamicParams(
  result: RouteResult,
  manifest: NextRouteTable,
): RouteResult {
  const target = result.invocationTarget;
  if (
    !target ||
    !result.routeMatches ||
    !result.resolvedPathname ||
    target.pathname !== result.resolvedPathname ||
    manifest.dispatch[result.resolvedPathname]?.kind === "prerender"
  ) {
    return result;
  }
  const query = { ...target.query };
  for (const key of Object.keys(result.routeMatches)) delete query[key];
  const { routeMatches: _routeMatches, ...rest } = result;
  return { ...rest, invocationTarget: { ...target, query } };
}

function queryFromUrl(url: URL): Record<string, string | string[]> {
  const query: Record<string, string | string[]> = {};
  for (const [key, value] of url.searchParams.entries()) {
    const existing = query[key];
    if (existing === undefined) query[key] = value;
    else query[key] = Array.isArray(existing) ? [...existing, value] : [existing, value];
  }
  return query;
}

export function ruleDestinationPathname(
  sourceRegex: string,
  destination: string,
  pathname: string,
): string | undefined {
  let match: RegExpMatchArray | null;
  try {
    match = pathname.match(new RegExp(sourceRegex));
  } catch {
    return undefined;
  }
  if (!match) return undefined;
  let out = destination.split("?")[0] ?? "";
  for (let i = match.length - 1; i >= 1; i--) {
    const group = match[i];
    if (group !== undefined) out = out.replace(new RegExp(`\\$${i}`, "g"), group);
  }
  if (match.groups) {
    const groups = Object.entries(match.groups).sort(([a], [b]) => b.length - a.length);
    for (const [key, value] of groups) {
      if (value !== undefined) out = out.replace(new RegExp(`\\$${key}`, "g"), value);
    }
  }
  return out;
}

function matchesConfigRewrite(
  pathname: string,
  resolvedPathname: string,
  routes: RoutingTable,
): boolean {
  const rules = [
    ...(routes.beforeFiles ?? []),
    ...(routes.afterFiles ?? []),
    ...(routes.fallback ?? []),
  ];
  const dynamicRoutes = routes.dynamicRoutes ?? [];
  for (const rule of rules) {
    if (!rule.destination) continue;
    const destination = ruleDestinationPathname(rule.sourceRegex, rule.destination, pathname);
    if (destination === undefined) continue;
    if (destination === resolvedPathname) return true;
    for (const dynamic of dynamicRoutes) {
      if (dynamic.destination?.split("?")[0] !== resolvedPathname) continue;
      try {
        if (new RegExp(dynamic.sourceRegex).test(destination)) return true;
      } catch {}
    }
  }
  return false;
}

export function withSourceInvocationTarget(
  result: RouteResult,
  routingUrl: URL,
  manifest: NextRouteTable,
): RouteResult {
  const target = result.invocationTarget;
  const routePath = target?.pathname ?? result.resolvedPathname;
  if (!target || !result.resolvedPathname) return { ...result, routePath };
  if (target.pathname === routingUrl.pathname) return { ...result, routePath };
  if (!matchesConfigRewrite(routingUrl.pathname, result.resolvedPathname, manifest.routes)) {
    return { ...result, routePath };
  }
  return {
    ...result,
    routePath,
    invocationTarget: {
      pathname: routingUrl.pathname,
      query: queryFromUrl(routingUrl),
    },
  };
}
