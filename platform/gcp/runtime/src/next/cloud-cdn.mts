import { type DispatchHost, dispatchRequest } from "@framework/next-runtime/dispatch-host";
import { fetchToNodeHandler } from "@framework/node-runtime/fetch-bridge";
import type { Invoke } from "@framework/node-runtime/host";
import { uncacheableCacheControl } from "@framework/node-runtime/origin-cache-control";
import { cacheTagsForCloudCdn, cloudCdnTagLimits } from "../cache/cloud-cdn-tags.mjs";

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
    rewritten.headers.set("cache-control", uncacheableCacheControl);
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
