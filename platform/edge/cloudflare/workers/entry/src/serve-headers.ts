import {
  type CacheDirectives,
  isStoredLifetime,
  readCacheDirectives,
  readDirectiveSeconds,
  readExpiresSeconds,
  readSharedLifetime,
  refusesSharedStore,
  sharedPolicyHeaders,
} from "@platform/edge-contract/cache-control";
import { buildPathCacheTag } from "@platform/edge-contract/cache-tags";
import { edgeCacheTagHeader } from "@platform/edge-contract/edge-cache";
import { purgeHeader } from "@platform/edge-contract/purge";

export const NOT_STORED = "private, no-store";

export const BROWSER_REVALIDATES = "public, max-age=0, must-revalidate";

export const ORIGIN_VARY = "x-ocel-vary";

const STALE_IF_ERROR_SECONDS = 86_400;

const MAX_TAGS = 1000;

const STORED_STATUSES = new Set([200, 301, 308, 404, 410]);

const KEYED_VARY = new Set([
  "accept-encoding",
  "rsc",
  "next-router-state-tree",
  "next-router-prefetch",
  "next-router-segment-prefetch",
  "next-url",
]);

export interface CachedResponse {
  release: string;
  pathname: string;
}

export interface CacheOptions {
  honoursAppCacheHeaders?: boolean;
}

function variesOffKey(headers: Headers): boolean {
  return (headers.get("vary") ?? "")
    .split(",")
    .map((name) => name.trim().toLowerCase())
    .some((name) => name !== "" && !KEYED_VARY.has(name));
}

function notStored(response: Response, options: CacheOptions): Response {
  const headers = new Headers(response.headers);
  headers.delete("cloudflare-cdn-cache-control");
  headers.delete("cdn-cache-control");
  if (options.honoursAppCacheHeaders) headers.set("cloudflare-cdn-cache-control", "no-store");
  else headers.set("cache-control", NOT_STORED);
  headers.delete("cache-tag");
  headers.delete(edgeCacheTagHeader);
  headers.delete(purgeHeader);
  return new Response(response.body, { status: response.status, headers });
}

export function withholdFromEdge(response: Response): Response {
  const headers = new Headers(response.headers);
  headers.set("cache-control", BROWSER_REVALIDATES);
  headers.delete("etag");
  return notStored(new Response(response.body, { status: response.status, headers }), {
    honoursAppCacheHeaders: true,
  });
}

function edgePolicy(headers: Headers): { source: string; directives: CacheDirectives } | undefined {
  for (const name of sharedPolicyHeaders) {
    const value = headers.get(name);
    if (value !== null) return { source: name, directives: readCacheDirectives(value) };
  }
  return undefined;
}

export function buildPathTag(release: string, pathname: string): string {
  return `${release}|${buildPathCacheTag(pathname)}`;
}

function releaseTags(headers: Headers, { release, pathname }: CachedResponse): string {
  const prefix = `${release}|`;
  const own = [release, buildPathTag(release, pathname)];
  const origin = (headers.get(edgeCacheTagHeader) ?? "")
    .split(",")
    .map((tag) => tag.trim())
    .filter((tag) => tag !== "")
    .map((tag) => (tag === release || tag.startsWith(prefix) ? tag : `${prefix}${tag}`))
    .filter((tag) => !own.includes(tag));
  return [...own, ...new Set(origin)].slice(0, MAX_TAGS).join(",");
}

export function forCache(
  response: Response,
  cached: CachedResponse,
  options: CacheOptions = {},
): Response {
  if (
    !STORED_STATUSES.has(response.status) ||
    response.headers.has("set-cookie") ||
    variesOffKey(response.headers)
  ) {
    return notStored(response, options);
  }
  const policy =
    edgePolicy(response.headers) ??
    (options.honoursAppCacheHeaders
      ? { source: "cache-control", directives: new Map() }
      : undefined);
  if (!policy || refusesSharedStore(readCacheDirectives(response.headers.get("cache-control")))) {
    return notStored(response, options);
  }
  const { directives } = policy;
  if (refusesSharedStore(directives)) return notStored(response, options);

  const lifetime = options.honoursAppCacheHeaders
    ? readSharedLifetime(
        directives,
        policy.source === "cache-control"
          ? readExpiresSeconds(response.headers, Date.now())
          : undefined,
      )
    : (readDirectiveSeconds(directives, "s-maxage") ??
      (policy.source === "cache-control"
        ? undefined
        : readDirectiveSeconds(directives, "max-age")));
  const staleWhileRevalidate = readDirectiveSeconds(directives, "stale-while-revalidate");
  if (lifetime === undefined || !isStoredLifetime(lifetime, staleWhileRevalidate)) {
    return notStored(response, options);
  }

  const headers = new Headers(response.headers);
  headers.set(
    "cloudflare-cdn-cache-control",
    [
      `max-age=${lifetime}`,
      ...(staleWhileRevalidate ? [`stale-while-revalidate=${staleWhileRevalidate}`] : []),
      `stale-if-error=${STALE_IF_ERROR_SECONDS}`,
    ].join(", "),
  );
  headers.delete("cdn-cache-control");
  if (
    !options.honoursAppCacheHeaders &&
    (policy.source === "cache-control" || !headers.has("cache-control"))
  ) {
    headers.set("cache-control", BROWSER_REVALIDATES);
  }
  headers.set("cache-tag", releaseTags(headers, cached));
  headers.delete(edgeCacheTagHeader);
  headers.delete(purgeHeader);
  const vary = headers.get("vary");
  if (vary !== null) headers.set(ORIGIN_VARY, vary);
  headers.delete("vary");
  return new Response(response.body, { status: response.status, headers });
}
