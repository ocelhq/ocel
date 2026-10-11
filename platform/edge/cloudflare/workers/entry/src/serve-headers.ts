import { buildPathCacheTag } from "@platform/edge-contract/cache-tags";
import { edgeCacheTagHeader } from "@platform/edge-contract/edge-cache";
import { purgeHeader } from "@platform/edge-contract/purge";

export const NOT_STORED = "private, no-store";

export const BROWSER_REVALIDATES = "public, max-age=0, must-revalidate";

export const ORIGIN_VARY = "x-ocel-vary";

const STALE_IF_ERROR_SECONDS = 86_400;

const MAX_TAGS = 1000;

const STORED_STATUSES = new Set([200, 301, 308, 404, 410]);

const EDGE_POLICY_HEADERS = ["cloudflare-cdn-cache-control", "cdn-cache-control"] as const;

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

type Directives = Map<string, string | true>;

function directivesOf(header: string | null): Directives {
  const directives: Directives = new Map();
  for (const part of header?.split(",") ?? []) {
    const [name, ...value] = part.trim().split("=");
    if (!name) continue;
    directives.set(name.toLowerCase(), value.length > 0 ? value.join("=").replace(/"/g, "") : true);
  }
  return directives;
}

function seconds(directives: Directives, name: string): number | undefined {
  const value = directives.get(name);
  if (typeof value !== "string" || !/^\d+$/.test(value)) return undefined;
  return Number(value);
}

function refusesStoring(directives: Directives): boolean {
  return directives.has("private") || directives.has("no-store") || directives.has("no-cache");
}

function variesOffKey(headers: Headers): boolean {
  return (headers.get("vary") ?? "")
    .split(",")
    .map((name) => name.trim().toLowerCase())
    .some((name) => name !== "" && !KEYED_VARY.has(name));
}

function notStored(response: Response): Response {
  const headers = new Headers(response.headers);
  headers.set("cache-control", NOT_STORED);
  for (const name of EDGE_POLICY_HEADERS) headers.delete(name);
  headers.delete("cache-tag");
  headers.delete(edgeCacheTagHeader);
  headers.delete(purgeHeader);
  return new Response(response.body, { status: response.status, headers });
}

function edgePolicy(headers: Headers): { source: string; directives: Directives } | undefined {
  for (const name of [...EDGE_POLICY_HEADERS, "cache-control"]) {
    const value = headers.get(name);
    if (value !== null) return { source: name, directives: directivesOf(value) };
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

export function forCache(response: Response, cached: CachedResponse): Response {
  if (
    !STORED_STATUSES.has(response.status) ||
    response.headers.has("set-cookie") ||
    variesOffKey(response.headers)
  ) {
    return notStored(response);
  }
  const policy = edgePolicy(response.headers);
  if (!policy || refusesStoring(directivesOf(response.headers.get("cache-control")))) {
    return notStored(response);
  }
  const { directives } = policy;
  if (refusesStoring(directives)) return notStored(response);

  const lifetime =
    seconds(directives, "s-maxage") ??
    (policy.source === "cache-control" ? undefined : seconds(directives, "max-age"));
  const staleWhileRevalidate = seconds(directives, "stale-while-revalidate");
  if (lifetime === undefined || (lifetime === 0 && !staleWhileRevalidate)) {
    return notStored(response);
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
  if (policy.source === "cache-control" || !headers.has("cache-control")) {
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
