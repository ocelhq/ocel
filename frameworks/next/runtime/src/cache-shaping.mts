import type http from "node:http";
import { storedCacheTags } from "@framework/next-cache";
import { dispatchesAtOrigin, invalidatesByCacheTag } from "@framework/node-runtime/host";
import { collectTags, notedTags } from "./origin-tags.mjs";
import { type ProjectManifest, walkPrerender } from "./project-manifest.mjs";
import type { RequestHeaders } from "./request-headers.mjs";

const cacheTagHeader = "cache-tag";

const releasePattern = /^r[0-9a-f]{8}$/;
const dataRoutePattern = /^\/_next\/data\/[^/]+\/(.*)\.json$/;

interface Window {
  revalidate: number;
  expire?: number;
}

export interface RevalidatingRoutes {
  basePath: string;
  routes: ReadonlyMap<string, Window>;
  dynamicRoutes: readonly { pattern: RegExp; window: Window }[];
}

export interface OriginShaping extends RevalidatingRoutes {
  release: string | null;
  tagsPerObject: number;
}

export function releaseOf(isrPrefix: string | undefined): string | null {
  const segments = isrPrefix?.split("/") ?? [];
  if (segments.length !== 5 || segments[4] !== "isr") return null;
  return releasePattern.test(segments[3]!) ? segments[3]! : null;
}

function windowOf(revalidate: unknown, expire: unknown): Window | null {
  if (typeof revalidate !== "number" || revalidate <= 0) return null;
  return {
    revalidate,
    ...(typeof expire === "number" && expire > revalidate && { expire }),
  };
}

export function revalidatingRoutes(manifest: ProjectManifest | null): RevalidatingRoutes {
  const { basePath, routes, dynamicRoutes } = walkPrerender(
    manifest,
    (entry) => windowOf(entry?.initialRevalidateSeconds, entry?.initialExpireSeconds),
    (entry) => windowOf(entry.fallbackRevalidate, entry.fallbackExpire),
  );
  return {
    basePath,
    routes,
    dynamicRoutes: dynamicRoutes.map(({ pattern, value }) => ({ pattern, window: value })),
  };
}

export function originShaping(
  routes: RevalidatingRoutes,
  env: NodeJS.ProcessEnv,
  tagsPerObject = Number.POSITIVE_INFINITY,
): OriginShaping | null {
  if (!dispatchesAtOrigin(env)) return null;

  return {
    ...routes,
    release: invalidatesByCacheTag(env) ? releaseOf(env.OCEL_ISR_PREFIX) : null,
    tagsPerObject,
  };
}

export function shapeOriginCache(
  req: http.IncomingMessage,
  res: http.ServerResponse,
  shaping: OriginShaping,
): void {
  collectTags(req.headers as RequestHeaders);
  const writeHead = res.writeHead;
  res.writeHead = function (this: http.ServerResponse, ...args: any[]) {
    if (!this.headersSent) shape(req, this, shaping);
    return (writeHead as any).apply(this, args);
  } as typeof res.writeHead;
}

function shape(req: http.IncomingMessage, res: http.ServerResponse, shaping: OriginShaping): void {
  if (!cacheable(req, res)) return;

  if (shaping.release !== null) {
    const noted = notedTags(req.headers as RequestHeaders);
    const { tags, unstorable, overflowed } = storedCacheTags(
      shaping.release,
      noted,
      shaping.tagsPerObject,
    );
    if (tags.length > 0) res.setHeader(cacheTagHeader, tags.join(","));
    const lost = [...unstorable, ...overflowed];
    if (lost.length > 0) {
      console.warn(
        `ocel: ${req.url} has ${noted.length} cache tags and the front stores ${shaping.tagsPerObject} that fit its alphabet, so revalidating ${lost.join(", ")} will not reach it`,
      );
    }
  }

  const declared = String(res.getHeader("cache-control") ?? "");
  if (personal(declared)) return;

  if (directives(declared).includes("s-maxage") || !isHtml(res)) return;
  const window = windowFor(req.url, shaping);
  if (window === undefined) return;
  res.setHeader("cache-control", cacheControlOf(window));
}

function cacheable(req: http.IncomingMessage, res: http.ServerResponse): boolean {
  if (req.method !== "GET" && req.method !== "HEAD") return false;
  if (res.statusCode !== 200) return false;
  return res.getHeader("set-cookie") === undefined;
}

function personal(declared: string): boolean {
  return directives(declared).some((name) => ["private", "no-store", "no-cache"].includes(name));
}

function directives(declared: string): string[] {
  return declared
    .toLowerCase()
    .split(",")
    .map((directive) => directive.trim().split("=")[0]!);
}

function isHtml(res: http.ServerResponse): boolean {
  return String(res.getHeader("content-type") ?? "").startsWith("text/html");
}

function cacheControlOf({ revalidate, expire }: Window): string {
  const swr = expire === undefined ? 0 : expire - revalidate;
  return swr > 0
    ? `s-maxage=${revalidate}, stale-while-revalidate=${swr}`
    : `s-maxage=${revalidate}`;
}

function windowFor(url: string | undefined, routes: RevalidatingRoutes): Window | undefined {
  const route = routeOf(url, routes.basePath);
  const exact = routes.routes.get(route);
  if (exact) return exact;
  return routes.dynamicRoutes.find(({ pattern }) => pattern.test(route))?.window;
}

export function routeOf(url: string | undefined, basePath: string): string {
  let pathname = (url ?? "/").split("?")[0]!;
  if (basePath !== "" && pathname.startsWith(basePath)) {
    pathname = pathname.slice(basePath.length) || "/";
  }
  const data = dataRoutePattern.exec(pathname);
  if (data) pathname = data[1] === "index" ? "/" : `/${data[1]}`;
  return pathname.length > 1 ? pathname.replace(/\/+$/, "") || "/" : pathname;
}
