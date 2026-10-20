import type http from "node:http";
import type { LauncherConfig, RevalidateWindow } from "@framework/next-protocol/launcher";
import { invalidatesByCacheTag } from "@framework/node-runtime/host";
import { beforeWriteHead } from "@framework/node-runtime/write-head";
import { storedCacheTags } from "@platform/edge-contract/cache-tags";
import {
  cachesOnlyMarked,
  edgeCacheHeader,
  edgeCacheTagHeader,
  readEdgeCacheMode,
} from "@platform/edge-contract/edge-cache";
import { readStorageCoordinate } from "@platform/edge-contract/storage-key";
import { collectTags, notedTags } from "./origin-tags.mjs";
import type { RequestHeaders } from "./request-headers.mjs";

const cacheTagHeader = "cache-tag";

const dataRoutePattern = /^\/_next\/data\/[^/]+\/(.*)\.json$/;

export interface RevalidatingRoutes {
  basePath: string;
  routes: ReadonlyMap<string, RevalidateWindow>;
  dynamicRoutes: readonly { pattern: RegExp; window: RevalidateWindow }[];
}

export interface OriginShaping extends RevalidatingRoutes {
  edgeCachesOnlyMarked: boolean;
  release: string | null;
  tagsPerObject: number;
}

export function releaseOf(env: NodeJS.ProcessEnv): string | null {
  return readStorageCoordinate(env)?.release ?? null;
}

export function revalidatingRoutes(config: LauncherConfig): RevalidatingRoutes {
  const dynamicRoutes: { pattern: RegExp; window: RevalidateWindow }[] = [];
  for (const { routeRegex, window } of config.cache.dynamicRoutes) {
    try {
      dynamicRoutes.push({ pattern: new RegExp(routeRegex), window });
    } catch (error) {
      console.error(`ocel: route pattern ${routeRegex} does not compile`, error);
    }
  }
  return {
    basePath: config.basePath,
    routes: new Map(Object.entries(config.cache.routes)),
    dynamicRoutes,
  };
}

export function originShaping(
  routes: RevalidatingRoutes,
  env: NodeJS.ProcessEnv,
  tagsPerObject = Number.POSITIVE_INFINITY,
): OriginShaping | null {
  const mode = readEdgeCacheMode(env);
  if (mode === undefined) return null;

  return {
    ...routes,
    edgeCachesOnlyMarked: cachesOnlyMarked(mode),
    release: invalidatesByCacheTag(env) ? releaseOf(env) : null,
    tagsPerObject,
  };
}

export function shapeOriginCache(
  req: http.IncomingMessage,
  res: http.ServerResponse,
  shaping: OriginShaping,
): void {
  collectTags(req.headers as RequestHeaders);
  beforeWriteHead(res, (res) => shape(req, res, shaping));
}

function shape(req: http.IncomingMessage, res: http.ServerResponse, shaping: OriginShaping): void {
  const asked = req.headers[edgeCacheHeader] === "1";
  if (shaping.edgeCachesOnlyMarked && !asked) return;
  if (!cacheable(req, res)) return;

  if (shaping.release !== null) {
    const noted = notedTags(req.headers as RequestHeaders);
    const { tags, unstorable, overflowed } = storedCacheTags(
      shaping.release,
      noted,
      shaping.tagsPerObject,
    );
    if (tags.length > 0) res.setHeader(asked ? edgeCacheTagHeader : cacheTagHeader, tags.join(","));
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
  const window = findRouteWindow(req.url, shaping);
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

function cacheControlOf({ revalidate, expire }: RevalidateWindow): string {
  const swr = expire === undefined ? 0 : expire - revalidate;
  return swr > 0
    ? `s-maxage=${revalidate}, stale-while-revalidate=${swr}`
    : `s-maxage=${revalidate}`;
}

export function findRouteWindow(
  url: string | undefined,
  routes: RevalidatingRoutes,
): RevalidateWindow | undefined {
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
