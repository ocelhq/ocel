import { releaseOf } from "@framework/next-runtime/cache-shaping";
import { type DispatchHost, dispatchRequest } from "@framework/next-runtime/dispatch-host";
import { fetchToNodeHandler } from "@framework/node-runtime/fetch-bridge";
import { type Invoke, invalidatesByCacheTag } from "@framework/node-runtime/host";

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

export const cloudCdnTagLimits = { perObject: 50, bytesPerTag: 120, bytesPerObject: 4096 } as const;

export const cloudCdnShapedTagsPerObject = cloudCdnTagLimits.perObject - 1;

export function cloudCdnRelease(env: NodeJS.ProcessEnv): string | null {
  return invalidatesByCacheTag(env) ? releaseOf(env.OCEL_ISR_PREFIX) : null;
}

function isImmutable(cacheControl: string | null): boolean {
  return (cacheControl ?? "")
    .split(",")
    .some((directive) => directive.trim().toLowerCase() === "immutable");
}

export function cacheTagsForCloudCdn(
  release: string | null,
  header: string | null,
  cacheControl: string | null,
): { value: string | null; dropped: string[] } {
  const own = new Set<string>();
  for (const token of (header ?? "").split(",")) {
    const tag = token.trim();
    if (tag !== "") own.add(tag);
  }
  const tagged = release !== null && !isImmutable(cacheControl);
  if (tagged) own.delete(release);
  const kept: string[] = [];
  const dropped: string[] = [];
  let bytes = 0;
  for (const tag of tagged ? [release, ...own] : own) {
    const joined = bytes + Buffer.byteLength(tag) + (kept.length > 0 ? 1 : 0);
    if (
      kept.length < cloudCdnTagLimits.perObject &&
      Buffer.byteLength(tag) <= cloudCdnTagLimits.bytesPerTag &&
      joined <= cloudCdnTagLimits.bytesPerObject
    ) {
      kept.push(tag);
      bytes = joined;
    } else {
      dropped.push(tag);
    }
  }
  return { value: kept.length > 0 ? kept.join(",") : null, dropped };
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
      `ocel: ${url} carries ${dropped.length} cache tags past Cloud CDN's limits, so revalidating ${dropped.join(", ")} will not reach it`,
    );
  }
  if (trimmedVary === vary && value === tags) return response;
  const rewritten = new Response(response.body, response);
  if (trimmedVary === null) rewritten.headers.delete("vary");
  else rewritten.headers.set("vary", trimmedVary);
  if (value === null) rewritten.headers.delete("cache-tag");
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
