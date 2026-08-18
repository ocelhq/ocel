import type http from "node:http";
import { collectTags, notedTags } from "./origin-tags.mjs";
import type { ProjectManifest } from "./project-manifest.mjs";
import { routerMode } from "../shared/edge-kind.mjs";

const cacheTagHeader = "cache-tag";
const nextCacheHeader = "x-nextjs-cache";
const staleCacheControl = "s-maxage=0, must-revalidate";

const releasePattern = /^r[0-9a-f]{8}$/;
const dataRoutePattern = /^\/_next\/data\/[^/]+\/(.*)\.json$/;

interface Window {
  revalidate: number;
  expire?: number;
}

export interface OriginShaping {
  release: string | null;
  basePath: string;
  routes: ReadonlyMap<string, Window>;
  dynamicRoutes: readonly { pattern: RegExp; window: Window }[];
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

export function originShaping(
  manifest: ProjectManifest | null,
  env: NodeJS.ProcessEnv,
): OriginShaping | null {
  if (!routerMode(env.OCEL_EDGE_KIND)) return null;

  const routes = new Map<string, Window>();
  for (const [route, entry] of Object.entries<any>(manifest?.prerender?.routes ?? {})) {
    const window = windowOf(entry?.initialRevalidateSeconds, entry?.initialExpireSeconds);
    if (window) routes.set(route, window);
  }

  const dynamicRoutes: { pattern: RegExp; window: Window }[] = [];
  for (const entry of Object.values<any>(manifest?.prerender?.dynamicRoutes ?? {})) {
    const window = windowOf(entry?.fallbackRevalidate, entry?.fallbackExpire);
    if (!window || typeof entry?.routeRegex !== "string") continue;
    try {
      dynamicRoutes.push({ pattern: new RegExp(entry.routeRegex), window });
    } catch {}
  }

  return {
    release: releaseOf(env.OCEL_ISR_PREFIX),
    basePath: typeof manifest?.config?.basePath === "string" ? manifest.config.basePath : "",
    routes,
    dynamicRoutes,
  };
}

export function shapeOriginCache(
  req: http.IncomingMessage,
  res: http.ServerResponse,
  shaping: OriginShaping,
): void {
  collectTags(req.headers as Record<string | symbol, any>);
  const writeHead = res.writeHead;
  res.writeHead = function (this: http.ServerResponse, ...args: any[]) {
    if (!this.headersSent) shape(req, this, shaping);
    return (writeHead as any).apply(this, args);
  } as typeof res.writeHead;
}

function shape(
  req: http.IncomingMessage,
  res: http.ServerResponse,
  shaping: OriginShaping,
): void {
  if (!cacheable(req, res)) return;

  const tags = notedTags(req.headers as Record<string | symbol, any>);
  if (shaping.release !== null && tags.length > 0) {
    res.setHeader(cacheTagHeader, tags.map((tag) => `${shaping.release}|${tag}`).join(","));
  }

  const declared = String(res.getHeader("cache-control") ?? "");
  if (personal(declared)) return;

  if (String(res.getHeader(nextCacheHeader) ?? "") === "STALE") {
    res.setHeader("cache-control", staleCacheControl);
    return;
  }

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
  return directives(declared).some((name) =>
    ["private", "no-store", "no-cache"].includes(name),
  );
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

function windowFor(
  url: string | undefined,
  shaping: OriginShaping,
): Window | undefined {
  const route = routeOf(url, shaping.basePath);
  const exact = shaping.routes.get(route);
  if (exact) return exact;
  return shaping.dynamicRoutes.find(({ pattern }) => pattern.test(route))?.window;
}

function routeOf(url: string | undefined, basePath: string): string {
  let pathname = (url ?? "/").split("?")[0]!;
  if (basePath !== "" && pathname.startsWith(basePath)) {
    pathname = pathname.slice(basePath.length) || "/";
  }
  const data = dataRoutePattern.exec(pathname);
  if (data) pathname = data[1] === "index" ? "/" : `/${data[1]}`;
  return pathname.length > 1 ? pathname.replace(/\/+$/, "") || "/" : pathname;
}
