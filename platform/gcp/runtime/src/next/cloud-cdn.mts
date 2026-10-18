import type http from "node:http";
import { type DispatchHost, dispatchRequest } from "@framework/next-runtime/dispatch-host";
import { fetchToNodeHandler } from "@framework/node-runtime/fetch-bridge";
import type { Invoke } from "@framework/node-runtime/host";
import { type HeaderLookup, isStoredBySomeEdge } from "@platform/edge-contract/cache-control";
import {
  cacheTagsForCloudCdn,
  cloudCdnTagLimits,
  isImmutable,
  uncacheable,
} from "../cache/cloud-cdn-tags.mjs";

const routerStateTree = "next-router-state-tree";

export function trimVaryForCloudCdn(vary: string | null): string | null {
  if (vary === null) return null;
  const seen = new Set<string>();
  const kept: string[] = [];
  for (const token of vary.split(",")) {
    const name = token.trim();
    const key = name.toLowerCase();
    if (name === "" || key === routerStateTree || seen.has(key)) continue;
    seen.add(key);
    kept.push(name);
  }
  return kept.length > 0 ? kept.join(", ") : null;
}

export const cloudCdnShapedTagsPerObject = cloudCdnTagLimits.perObject - 1;

export function isWithheldFromCloudCdn(
  headers: HeaderLookup,
  now: number,
  edgeCacheControlHeader: string | undefined,
): boolean {
  return (
    !isImmutable(headers.get("cache-control")) &&
    isStoredBySomeEdge(headers, now, edgeCacheControlHeader)
  );
}

function readWriteHeadHeader(headers: unknown, name: string): string | null {
  if (Array.isArray(headers)) {
    for (let i = 0; i + 1 < headers.length; i += 2) {
      if (String(headers[i]).toLowerCase() === name) return String(headers[i + 1]);
    }
    return null;
  }
  if (headers !== null && typeof headers === "object") {
    for (const [key, value] of Object.entries(headers)) {
      if (key.toLowerCase() === name) return String(value);
    }
  }
  return null;
}

function withoutWriteHeadCacheControl(headers: unknown): unknown {
  if (Array.isArray(headers)) {
    const kept: unknown[] = [];
    for (let i = 0; i + 1 < headers.length; i += 2) {
      if (String(headers[i]).toLowerCase() !== "cache-control")
        kept.push(headers[i], headers[i + 1]);
    }
    return kept;
  }
  if (headers !== null && typeof headers === "object") {
    return Object.fromEntries(
      Object.entries(headers).filter(([key]) => key.toLowerCase() !== "cache-control"),
    );
  }
  return headers;
}

export function withholdResponsesFromCloudCdn(
  prototype: http.ServerResponse,
  edgeCacheControlHeader: string | undefined,
): void {
  const writeHead = prototype.writeHead;
  prototype.writeHead = function (this: http.ServerResponse, ...args: any[]) {
    if (!this.headersSent) {
      const headersAt = typeof args[1] === "string" ? 2 : 1;
      const passed = args.length > headersAt ? args[headersAt] : undefined;
      const headers = {
        get: (name: string): string | null => {
          const value = readWriteHeadHeader(passed, name) ?? this.getHeader(name);
          return value === undefined || value === null ? null : String(value);
        },
      };
      if (isWithheldFromCloudCdn(headers, Date.now(), edgeCacheControlHeader)) {
        this.setHeader("cache-control", uncacheable);
        if (passed !== undefined) args[headersAt] = withoutWriteHeadCacheControl(passed);
      }
    }
    return (writeHead as any).apply(this, args);
  } as typeof prototype.writeHead;
}

export function forCloudCdn(response: Response, release: string | null, url: string): Response {
  const vary = response.headers.get("vary");
  const trimmedVary = trimVaryForCloudCdn(vary);
  const tags = response.headers.get("cache-tag");
  const { value, dropped } = cacheTagsForCloudCdn(
    release,
    tags,
    response.headers.get("cache-control"),
  );
  if (dropped.length > 0) {
    console.warn(
      `ocel: ${url} carries ${dropped.length} cache tags past Cloud CDN's limits, so Cloud CDN does not store it and revalidating ${dropped.join(", ")} cannot miss it`,
    );
  }
  if (trimmedVary === vary && value === tags && dropped.length === 0) return response;
  const rewritten = new Response(response.body, response);
  if (trimmedVary === null) rewritten.headers.delete("vary");
  else rewritten.headers.set("vary", trimmedVary);
  if (dropped.length > 0) {
    rewritten.headers.set("cache-control", uncacheable);
    rewritten.headers.delete("cache-tag");
  } else if (value === null) rewritten.headers.delete("cache-tag");
  else rewritten.headers.set("cache-tag", value);
  return rewritten;
}

export async function dispatchForCloudCdn(
  request: Request,
  host: DispatchHost,
  release: string | null,
  waitUntil: (promise: Promise<unknown>) => void,
): Promise<Response> {
  return forCloudCdn(await dispatchRequest(request, host, waitUntil), release, request.url);
}

export function newCloudCdnDispatchInvoke(host: DispatchHost, release: string | null): Invoke {
  return (req, res, ocel) =>
    fetchToNodeHandler((request) => dispatchForCloudCdn(request, host, release, ocel.waitUntil))(
      req,
      res,
      ocel,
    );
}
