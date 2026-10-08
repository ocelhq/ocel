import type http from "node:http";
import { refreshHeader } from "@framework/next-cache";
import {
  findPrerenderedRoute,
  type PrerenderedRoute,
  type PrerenderedRoutes,
} from "./prerendered-routes.mjs";
import type { RequestHeaders } from "./request-headers.mjs";

export interface StaleEntry {
  key: string;
  lastModified: number;
}

export interface Refresh {
  url: string;
  key: string;
  lastModified: number;
  headers: Record<string, string>;
}

export type ScheduleRefresh = (refresh: Refresh) => Promise<void>;

export interface ServedRoute {
  revalidate: number | false;
  readsNoEntry: boolean;
}

const staleEntryKey = Symbol.for("ocel.next.stale-entry.v2");

const servedRouteKey = Symbol.for("ocel.next.served-route.v1");

const prefetchPurpose = "prefetch";

const rscQuery = "_rsc";

export function noteStaleEntry(headers: RequestHeaders, entry: StaleEntry): void {
  const noted = readStaleEntry(headers);
  if (noted === undefined || entry.lastModified > noted.lastModified)
    headers[staleEntryKey] = entry;
}

export function readStaleEntry(headers: RequestHeaders): StaleEntry | undefined {
  const noted = headers[staleEntryKey];
  return typeof noted === "object" &&
    noted !== null &&
    typeof noted.key === "string" &&
    Number.isFinite(noted.lastModified)
    ? noted
    : undefined;
}

export function noteServedRoute(headers: RequestHeaders, route: ServedRoute): void {
  headers[servedRouteKey] = route;
}

export function readServedRoute(headers: RequestHeaders): ServedRoute | undefined {
  return headers[servedRouteKey];
}

function pageUrl(url: string | undefined): string {
  const parsed = new URL(url ?? "/", "http://origin");
  parsed.searchParams.delete(rscQuery);
  return parsed.pathname + parsed.search;
}

export function resumesFromPageEntry(
  req: http.IncomingMessage,
  route: PrerenderedRoute,
  cacheComponents: boolean,
): boolean {
  if (!cacheComponents || !route.partiallyStatic) return false;
  const contentType = req.headers["content-type"] ?? "";
  const isDynamicNavigation =
    (req.method === "GET" || req.method === "HEAD") &&
    req.headers.rsc === "1" &&
    req.headers["next-router-prefetch"] !== "1" &&
    !route.hasPrefetchData;
  const isServerAction =
    req.method === "POST" &&
    (typeof req.headers["next-action"] === "string" ||
      contentType === "application/x-www-form-urlencoded" ||
      contentType.startsWith("multipart/form-data"));
  return isDynamicNavigation || isServerAction;
}

export function routeStaleHitsToRefresh(
  req: http.IncomingMessage,
  res: http.ServerResponse,
  routes: PrerenderedRoutes,
  schedule: ScheduleRefresh,
  waitUntil: (promise: Promise<unknown>) => void,
): void {
  if (req.headers[refreshHeader] !== undefined) return;
  const route = findPrerenderedRoute(req.url, routes);
  if (!route) return;
  const readsNoEntry = resumesFromPageEntry(req, route, routes.cacheComponents);
  const isRead = req.method === "GET" || req.method === "HEAD";
  if (!isRead && !readsNoEntry) return;
  if (isRead) req.headers.purpose = prefetchPurpose;
  noteServedRoute(req.headers as RequestHeaders, { revalidate: route.revalidate, readsNoEntry });

  const writeHead = res.writeHead;
  res.writeHead = function (this: http.ServerResponse, ...args: any[]) {
    const stale = readStaleEntry(req.headers as RequestHeaders);
    if (!this.headersSent && stale !== undefined) {
      const refresh: Refresh = {
        url: pageUrl(req.url),
        key: stale.key,
        lastModified: stale.lastModified,
        headers: {
          ...(req.headers.host ? { host: req.headers.host } : {}),
          [refreshHeader]: String(stale.lastModified),
        },
      };
      waitUntil(
        Promise.resolve()
          .then(() => schedule(refresh))
          .catch((err) => {
            console.warn(`ocel: could not schedule a refresh of ${refresh.url}: ${String(err)}`);
          }),
      );
    }
    return (writeHead as any).apply(this, args);
  } as typeof res.writeHead;
}
