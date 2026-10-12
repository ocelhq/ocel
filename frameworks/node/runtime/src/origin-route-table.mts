import { readFileSync } from "node:fs";
import type http from "node:http";
import { type Bypass, readBypass, revalidateHeader } from "@platform/edge-contract/bypass";
import {
  addRedirectQuery,
  type CacheRule,
  type CompiledRouteTable,
  compileOcelRouteTable,
  findHeaders,
  findRedirect,
  findRoute,
  findSlashRedirect,
  ocelRouteTableVar,
  parseOcelRouteTable,
} from "@platform/edge-contract/ocel-route-table";
import { type OcelContext, setRequestScope } from "./host.mjs";
import {
  installCacheHost,
  type OriginCache,
  openRequestCache,
  prepareCachedResponse,
  raisePathPurge,
  readOriginCache,
  removeCacheHost,
  runWithRequestCache,
  watchRequestCache,
} from "./origin-cache.mjs";

interface Origin {
  routes: CompiledRouteTable | undefined;
  cache: OriginCache | null;
}

export interface RoutedRequest {
  pathname: string;
  headers: Record<string, string>;
  cacheRule: CacheRule | undefined;
  bypass: Bypass;
  redirect: { status: number; location: string } | undefined;
}

function readOriginRouteTable(env: NodeJS.ProcessEnv): CompiledRouteTable | undefined {
  const file = env[ocelRouteTableVar];
  if (!file) return undefined;
  let text: string;
  try {
    text = readFileSync(file, "utf8");
  } catch (error) {
    throw new Error(
      `ocel: ${ocelRouteTableVar} names ${file}, which cannot be read: ${error instanceof Error ? error.message : String(error)}`,
    );
  }
  return compileOcelRouteTable(parseOcelRouteTable(text));
}

export function useOriginRouteTable(env: NodeJS.ProcessEnv): void {
  const origin: Origin = { routes: readOriginRouteTable(env), cache: readOriginCache(env) };
  if (origin.cache) installCacheHost(origin.cache);
  else removeCacheHost();
  setRequestScope(
    origin.routes || origin.cache
      ? (req, res, ocel, run) => runInRequest(origin, req, res, ocel, run)
      : undefined,
  );
}

function splitTarget(url: string): { pathname: string; query: string } {
  const at = url.indexOf("?");
  return at === -1
    ? { pathname: url, query: "" }
    : { pathname: url.slice(0, at), query: url.slice(at) };
}

function isRead(req: http.IncomingMessage): boolean {
  return req.method === "GET" || req.method === "HEAD";
}

function routeRequest(origin: Origin, req: http.IncomingMessage, pathname: string): RoutedRequest {
  const { routes } = origin;
  const route = routes ? findRoute(routes, pathname) : undefined;
  const cacheRule = origin.cache ? route?.route.cache : undefined;
  const slash = routes && isRead(req) ? findSlashRedirect(routes, pathname) : undefined;
  const redirect = slash
    ? { status: 308, location: slash }
    : routes
      ? findRedirect(routes, pathname)
      : undefined;
  const header = req.headers[revalidateHeader];
  return {
    pathname,
    headers: routes ? findHeaders(routes, pathname) : {},
    cacheRule,
    bypass: cacheRule
      ? readBypass({
          token: cacheRule.bypassToken,
          method: req.method ?? "GET",
          cookie: req.headers.cookie,
          revalidate: Array.isArray(header) ? header[0] : header,
        })
      : "none",
    redirect,
  };
}

const lifetimeHeaders = ["cache-control", "cdn-cache-control", "surrogate-control"];

function applyHeaderRules(res: http.ServerResponse, routed: RoutedRequest): void {
  for (const [name, value] of Object.entries(routed.headers)) {
    if (routed.cacheRule && lifetimeHeaders.includes(name)) continue;
    res.setHeader(name, value);
  }
}

function answerRedirect(
  res: http.ServerResponse,
  routed: RoutedRequest,
  redirect: { status: number; location: string },
  query: string,
): void {
  applyHeaderRules(res, routed);
  const location = addRedirectQuery(redirect.location, query);
  res.writeHead(redirect.status, { location });
  res.end();
}

function dropCookie(req: http.IncomingMessage): void {
  delete req.headers.cookie;
  const kept: string[] = [];
  for (let i = 0; i + 1 < req.rawHeaders.length; i += 2) {
    if (req.rawHeaders[i]!.toLowerCase() !== "cookie")
      kept.push(req.rawHeaders[i]!, req.rawHeaders[i + 1]!);
  }
  req.rawHeaders = kept;
}

function declareHeaders(res: http.ServerResponse, declared: unknown): void {
  if (Array.isArray(declared)) {
    const pairs = Array.isArray(declared[0]) ? declared : pairFlatHeaders(declared);
    for (const [name, value] of pairs) res.setHeader(name, value);
  } else if (declared !== null && typeof declared === "object") {
    for (const [name, value] of Object.entries(declared)) {
      if (value !== undefined) res.setHeader(name, value as string | string[]);
    }
  }
}

function pairFlatHeaders(flat: unknown[]): [string, string][] {
  const pairs: [string, string][] = [];
  for (let i = 0; i + 1 < flat.length; i += 2) pairs.push([String(flat[i]), String(flat[i + 1])]);
  return pairs;
}

function runInRequest<T>(
  origin: Origin,
  req: http.IncomingMessage,
  res: http.ServerResponse,
  ocel: OcelContext,
  run: () => T,
): T {
  const { pathname, query } = splitTarget(req.url ?? "/");
  const routed = routeRequest(origin, req, pathname);
  if (routed.redirect) {
    answerRedirect(res, routed, routed.redirect, query);
    return undefined as T;
  }
  if (origin.cache && routed.cacheRule && routed.bypass !== "bypass" && isRead(req)) {
    dropCookie(req);
  }
  const store = origin.cache ? openRequestCache(res, ocel) : undefined;
  const writeHead = res.writeHead;
  res.writeHead = function (this: http.ServerResponse, ...args: any[]) {
    if (this.headersSent) return (writeHead as any).apply(this, args);
    const [status, ...rest] = args;
    if (typeof status === "number") this.statusCode = status;
    const message = typeof rest[0] === "string" ? [rest.shift()] : [];
    declareHeaders(this, rest[0]);
    applyHeaderRules(this, routed);
    if (origin.cache && store) prepareCachedResponse(origin.cache, req, this, store, routed);
    return (writeHead as any).apply(this, [status, ...message]);
  } as typeof res.writeHead;
  if (!origin.cache || !store) return run();
  if (routed.bypass === "revalidate") raisePathPurge(origin.cache, store, pathname);
  watchRequestCache(origin.cache, res, store);
  return runWithRequestCache(store, run);
}
