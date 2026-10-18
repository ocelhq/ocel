import type { NextRouteTable } from "@framework/next-protocol/route-table";
import { dropEmptyBodySentinel } from "@platform/edge-contract/empty-body";
import { purgeHeader, readPurge } from "@platform/edge-contract/purge";
import { encodeForwardedSearch } from "@platform/edge-contract/request-path";
import { ENTRY_HEADER } from "./control-headers.mjs";
import type { RouteDeps } from "./index.mjs";
import { retainOwner } from "./origin-response.mjs";
import { encodeRequestTarget } from "./request-target.mjs";
import type { RouteResult } from "./route-result.mjs";
import { canonicalPathname, withoutBasePath } from "./trailing-slash.mjs";

type HeadersInit = NonNullable<ConstructorParameters<typeof Headers>[0]>;

type BodyInit = NonNullable<RequestInit["body"]>;

type RequestInfo = Parameters<typeof fetch>[0];

const NEXT_ACTION_REVALIDATED = "x-action-revalidated";

function dataPathname(pathname: string, url: URL, manifest: NextRouteTable): string {
  const base = manifest.basePath ?? "";
  const prefix = `${base}/_next/data/${manifest.buildId}`;
  if (!url.pathname.startsWith(`${prefix}/`) || !url.pathname.endsWith(".json")) {
    return pathname;
  }
  if (pathname.startsWith(`${prefix}/`)) return pathname;
  const route = (withoutBasePath(pathname, base) ?? pathname).replace(/\/$/, "");
  return `${prefix}${route || "/index"}.json`;
}

function searchFromQuery(query: Record<string, string | string[]>): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    for (const v of Array.isArray(value) ? value : [value]) {
      if (v === `$${key}`) continue;
      params.append(key, v);
    }
  }
  const search = params.toString();
  return search ? `?${search}` : "";
}

export function originUrl(
  fnUrl: string,
  url: URL,
  result: RouteResult,
  manifest: NextRouteTable,
): URL {
  const pathname = result.invocationTarget?.pathname ?? url.pathname;
  const query = result.invocationTarget?.query;
  const search = query ? searchFromQuery(query) : url.search;
  const target = canonicalPathname(dataPathname(pathname, url, manifest), manifest);
  return new URL(encodeRequestTarget(target) + encodeForwardedSearch(search), fnUrl);
}

export async function bufferBody(request: Request): Promise<ArrayBuffer | null> {
  if (!request.body || request.method === "GET" || request.method === "HEAD") {
    return null;
  }
  return request.arrayBuffer();
}

export function streamOf(body: ArrayBuffer | null): ReadableStream | null {
  return body === null ? null : new Blob([body]).stream();
}

const NEXT_CACHE_TAGS_HEADER = "x-next-cache-tags";
const CACHE_TAG_HEADER = "cache-tag";

export function originFetch(deps: RouteDeps): typeof fetch {
  const doFetch = deps.originFetch ?? deps.fetch ?? fetch;
  return (async (input, init) => {
    const response = dropEmptyBodySentinel(retainOwner(await doFetch(input as RequestInfo, init)));
    const keepCacheTags = deps.keepCacheTags === true;
    const hasCacheTags =
      response.headers.has(NEXT_CACHE_TAGS_HEADER) ||
      (!keepCacheTags && response.headers.has(CACHE_TAG_HEADER));
    const announced = response.headers.get(purgeHeader);
    if (!hasCacheTags && announced === null) return response;

    if (announced !== null) {
      await deps.onPurge?.(readPurge(announced), response.headers.has(NEXT_ACTION_REVALIDATED));
    }
    const rebuilt = new Response(response.body, response);
    rebuilt.headers.delete(NEXT_CACHE_TAGS_HEADER);
    if (!keepCacheTags) rebuilt.headers.delete(CACHE_TAG_HEADER);
    rebuilt.headers.delete(purgeHeader);
    return rebuilt;
  }) as typeof fetch;
}

const ORIGIN_DATA_PATH = /\/_next\/data\/[^/]+\/.*\.json$/;

export function forward(
  url: URL,
  request: Request,
  headers: HeadersInit,
  body: BodyInit | null = request.body,
): Request {
  const publicUrl = new URL(request.url);
  const forwarded = new Headers(headers);
  forwarded.set("x-forwarded-host", publicUrl.host);
  forwarded.set("x-forwarded-proto", publicUrl.protocol.replace(/:$/, ""));
  if (ORIGIN_DATA_PATH.test(url.pathname)) forwarded.set("x-nextjs-data", "1");
  const init: RequestInit & { duplex?: "half" } = {
    method: request.method,
    headers: forwarded,
    body,
    redirect: "manual",
    ...(body === null ? {} : { duplex: "half" as const }),
  };
  return new Request(url, init);
}

export function withEntry(request: Request, entryKey: string | undefined): Request {
  const headers = new Headers(request.headers);
  if (entryKey !== undefined) headers.set(ENTRY_HEADER, entryKey);
  else headers.delete(ENTRY_HEADER);
  return new Request(request, { headers });
}

const MIDDLEWARE_HEADERS_HEADER = "x-ocel-middleware-headers";

export function originAuthoredResponse(response: Response): Response {
  const declared = response.headers.get(MIDDLEWARE_HEADERS_HEADER);
  const allow = new Set(
    (declared ?? "")
      .split(",")
      .map((name) => name.trim().toLowerCase())
      .filter(Boolean),
  );
  const headers = new Headers();
  response.headers.forEach((value, name) => {
    const lower = name.toLowerCase();
    if (lower === "set-cookie" || lower === MIDDLEWARE_HEADERS_HEADER) return;
    if (lower.startsWith("x-middleware-") || allow.has(lower)) headers.set(name, value);
  });
  if (allow.has("set-cookie")) {
    for (const cookie of response.headers.getSetCookie()) headers.append("set-cookie", cookie);
  }
  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers,
  });
}
