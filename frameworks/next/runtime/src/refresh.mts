import type http from "node:http";
import { refreshHeader } from "@framework/next-cache";
import { isRevalidatingRoute, nextCacheHeader, type RevalidatingRoutes } from "./cache-shaping.mjs";
import type { RequestHeaders } from "./request-headers.mjs";

export interface Refresh {
  url: string;
  lastModified: number;
  headers: Record<string, string>;
}

export type ScheduleRefresh = (refresh: Refresh) => Promise<void>;

const servedEntryKey = Symbol.for("ocel.next.served-entry.v1");

const prefetchPurpose = "prefetch";

const rscQuery = "_rsc";

export function noteServedEntry(headers: RequestHeaders, lastModified: number): void {
  const noted = headers[servedEntryKey];
  headers[servedEntryKey] =
    typeof noted === "number" ? Math.max(noted, lastModified) : lastModified;
}

export function readServedEntry(headers: RequestHeaders): number | undefined {
  const noted = headers[servedEntryKey];
  return typeof noted === "number" ? noted : undefined;
}

function pageUrl(url: string | undefined): string {
  const parsed = new URL(url ?? "/", "http://origin");
  parsed.searchParams.delete(rscQuery);
  return parsed.pathname + parsed.search;
}

function servesStaleAsIs(req: http.IncomingMessage, routes: RevalidatingRoutes): boolean {
  if (req.method !== "GET" && req.method !== "HEAD") return false;
  if (req.headers[refreshHeader] !== undefined) return false;
  return isRevalidatingRoute(req.url, routes);
}

export function routeStaleHitsToRefresh(
  req: http.IncomingMessage,
  res: http.ServerResponse,
  routes: RevalidatingRoutes,
  schedule: ScheduleRefresh,
  holdEnd: (promise: Promise<unknown>) => void,
): void {
  if (!servesStaleAsIs(req, routes)) return;
  req.headers.purpose = prefetchPurpose;

  const writeHead = res.writeHead;
  res.writeHead = function (this: http.ServerResponse, ...args: any[]) {
    const lastModified = readServedEntry(req.headers as RequestHeaders);
    if (
      !this.headersSent &&
      lastModified !== undefined &&
      String(this.getHeader(nextCacheHeader) ?? "") === "STALE"
    ) {
      const refresh: Refresh = {
        url: pageUrl(req.url),
        lastModified,
        headers: {
          ...(req.headers.host ? { host: req.headers.host } : {}),
          [refreshHeader]: String(lastModified),
        },
      };
      holdEnd(
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
