import type { DispatchTarget, NextRouteTable } from "@framework/next-protocol/route-table";
import { encodeForwardedSearch, isAbsoluteUrl } from "@platform/edge-contract/request-path";
import { isImmutablePathname, serveStaticAsset } from "./assets.mjs";
import { withoutControlHeaders } from "./control-headers.mjs";
import { withStatus } from "./http-cache.mjs";
import { localeOf } from "./i18n.mjs";
import type { PrerenderTarget, RouteDeps } from "./index.mjs";
import { middlewareOverrides, middlewareResponse } from "./middleware.mjs";
import {
  forward,
  originAuthoredResponse,
  originFetch,
  originUrl,
  withEntry,
} from "./origin-request.mjs";
import { retainOwner } from "./origin-response.mjs";
import { retryTransientOrigin } from "./retry.mjs";
import type { RouteResult } from "./route-result.mjs";
import { asSegmentPayload, isSegmentPrefetch } from "./segment.mjs";
import { isNextDataPathname, middlewareMatchPathname } from "./trailing-slash.mjs";

type RedirectStatus = Parameters<typeof Response.redirect>[1];

const REDIRECT_STATUSES = new Set([301, 302, 303, 307, 308]);

function redirectStatus(status: number): RedirectStatus | undefined {
  return REDIRECT_STATUSES.has(status) ? (status as RedirectStatus) : undefined;
}

const FLIGHT_VARY_HEADERS = [
  "rsc",
  "next-router-state-tree",
  "next-router-prefetch",
  "next-router-segment-prefetch",
  "next-url",
];

function isFlightRequest(headers: Headers): boolean {
  return FLIGHT_VARY_HEADERS.some((name) => headers.has(name));
}

function withFlightVary(response: Response): Response {
  const tagged = new Response(response.body, response);
  tagged.headers.set("vary", FLIGHT_VARY_HEADERS.join(", "));
  return tagged;
}

export function forwardRequestQuery(headers: Headers, requestUrl: string): void {
  const location = headers.get("location");
  if (!location) return;
  if (!location.startsWith("/") && !isAbsoluteUrl(location)) return;

  const requestQueryIndex = requestUrl.indexOf("?");
  if (requestQueryIndex === -1) return;
  const requestQuery = requestUrl.slice(requestQueryIndex + 1);
  if (!requestQuery) return;

  const hashIndex = location.indexOf("#");
  const addressed = hashIndex === -1 ? location : location.slice(0, hashIndex);
  const hash = hashIndex === -1 ? "" : location.slice(hashIndex);
  const queryIndex = addressed.indexOf("?");
  const path = queryIndex === -1 ? addressed : addressed.slice(0, queryIndex);
  const query = queryIndex === -1 ? "" : addressed.slice(queryIndex + 1);

  const declared = new Set(new URLSearchParams(query).keys());
  const forwarded = requestQuery
    .split("&")
    .filter((pair) => pair !== "" && !declared.has(queryKey(pair)));
  if (forwarded.length === 0) return;

  const merged = query ? [query, ...forwarded] : forwarded;
  headers.set("location", `${path}?${merged.join("&")}${hash}`);
}

function queryKey(pair: string): string {
  const raw = pair.split("=", 1)[0]!.replaceAll("+", " ");
  try {
    return decodeURIComponent(raw);
  } catch {
    return raw;
  }
}

export function applyResolvedHeaders(target: Headers, resolved: Headers | undefined): void {
  if (!resolved) return;
  resolved.forEach((value, name) => {
    if (name.toLowerCase() !== "set-cookie") target.set(name, value);
  });
  for (const cookie of resolved.getSetCookie()) {
    target.append("set-cookie", cookie);
  }
}

const MIDDLEWARE_PREFETCH_HEADER = "x-middleware-prefetch";

function middlewarePrefetchProbe(
  request: Request,
  pathname: string,
  manifest: NextRouteTable,
): Response | undefined {
  if (!request.headers.has(MIDDLEWARE_PREFETCH_HEADER)) return undefined;
  if (!isNextDataPathname(pathname, manifest, manifest.buildId)) return undefined;

  return new Response("{}", {
    status: 200,
    headers: {
      "content-type": "application/json",
      "x-matched-path": middlewareMatchPathname(pathname, manifest, manifest.buildId),
      "x-middleware-skip": "1",
      "cache-control": "private, no-cache, no-store, max-age=0, must-revalidate",
    },
  });
}

function withoutAssetPrefix(url: URL, manifest: NextRouteTable): URL {
  const prefix = manifest.assetPrefixPathname;
  if (!prefix || !url.pathname.startsWith(`${prefix}/_next/`)) return url;
  const unprefixed = new URL(url);
  unprefixed.pathname = `${manifest.basePath}${url.pathname.slice(prefix.length)}`;
  return unprefixed;
}

export async function dispatch(
  result: RouteResult,
  request: Request,
  deps: RouteDeps,
): Promise<Response> {
  const { manifest } = deps;
  const doFetch = deps.fetch ?? fetch;
  const url = new URL(request.url);
  const headers = withoutControlHeaders(result.middleware?.headers ?? request.headers);

  const assetUrl =
    manifest.i18n && result.invocationTarget
      ? new URL(result.invocationTarget.pathname + url.search, url)
      : url;
  const staticAsset = (at: URL = assetUrl) =>
    serveStaticAsset(
      request,
      at,
      deps.assetStore,
      manifest.i18n && localeOf(manifest.i18n, manifest.basePath, url),
    );

  if (result.middlewareResponded) {
    return middlewareResponse(result.middleware, result.status);
  }
  if (result.redirect) {
    const status = redirectStatus(result.redirect.status);
    if (status === undefined) return new Response(null, { status: 500 });
    return Response.redirect(result.redirect.url.toString(), status);
  }
  if (isRoutingRedirect(result)) {
    return new Response(null, { status: result.status });
  }
  if (result.externalRewrite) {
    return retainOwner(await doFetch(new Request(result.externalRewrite, request)));
  }
  if (!result.resolvedPathname) {
    const probe = middlewarePrefetchProbe(request, url.pathname, manifest);
    if (probe) return probe;
    const unprefixedAssetUrl = withoutAssetPrefix(assetUrl, manifest);
    const asset = await staticAsset(unprefixedAssetUrl);
    if (asset.status !== 404) return asset;
    if (isNextDataPathname(url.pathname, manifest, manifest.buildId)) return asset;
    if (isImmutablePathname(deps.assetStore, unprefixedAssetUrl.pathname)) return asset;
    return notFoundResponse(request, url, result, headers, deps, () => asset, staticAsset);
  }

  if (hasUndecodableRouteMatch(result.routeMatches)) {
    return new Response("Bad Request", { status: 400 });
  }

  const target = manifest.dispatch[result.resolvedPathname];
  if (!target) {
    return notFoundResponse(request, url, result, headers, deps, staticAsset, staticAsset);
  }

  if (target.kind === "function") {
    const probe = middlewarePrefetchProbe(request, url.pathname, manifest);
    if (probe) return probe;
  }

  const disallowed = documentMethodNotAllowed(request, target);
  if (disallowed) return disallowed;

  const response = await renderDispatchTarget(
    target,
    request,
    url,
    result,
    headers,
    deps,
    staticAsset,
  );
  return target.kind === "function" && target.page
    ? substituteErrorPage(response, request, url, headers, manifest, deps)
    : response;
}

const SERVER_ACTION_CONTENT_TYPES = ["application/x-www-form-urlencoded", "multipart/form-data"];

function isServerAction(request: Request): boolean {
  if (request.method !== "POST") return false;
  if (request.headers.has("next-action")) return true;
  const contentType = request.headers.get("content-type") ?? "";
  const media = (contentType.split(";")[0] ?? "").trim().toLowerCase();
  return SERVER_ACTION_CONTENT_TYPES.includes(media);
}

function documentMethodNotAllowed(request: Request, target: DispatchTarget): Response | undefined {
  if (target.kind !== "static" && target.kind !== "prerender") return undefined;
  if (request.method === "GET" || request.method === "HEAD") return undefined;
  if (isServerAction(request)) return undefined;
  return new Response("Method Not Allowed", {
    status: 405,
    headers: { allow: "GET, HEAD" },
  });
}

async function renderDispatchTarget(
  target: DispatchTarget,
  request: Request,
  url: URL,
  result: RouteResult,
  headers: Headers,
  deps: RouteDeps,
  staticAsset: (at?: URL) => Promise<Response>,
): Promise<Response> {
  const { manifest, functionUrls } = deps;
  const doOrigin = originFetch(deps);

  switch (target.kind) {
    case "static": {
      const response = await staticAsset(new URL(result.resolvedPathname!, url));
      return isFlightRequest(request.headers) ? withFlightVary(response) : response;
    }

    case "function": {
      const fnUrl = functionUrls[target.id];
      if (!fnUrl) return noFunctionUrl(target.id);
      return withStatus(
        await doOrigin(
          withEntry(
            forward(originUrl(fnUrl, url, result, manifest), request, headers),
            target.entryKey,
          ),
        ),
        "MISS",
      );
    }

    case "prerender":
      return prerenderResponse(request, url, result, target, headers, deps);

    case "edge": {
      if (!target.entryKey) return noEdgeEntry(result.resolvedPathname);
      return withStatus(
        await edgeResponse(
          deps,
          target.entryKey,
          forward(originUrl(url.origin, url, result, manifest), request, headers),
        ),
        "MISS",
      );
    }

    default: {
      const unknown: never = target;
      throw new Error(
        `ocel: the route table dispatches "${result.resolvedPathname}" to target kind "${(unknown as { kind: unknown }).kind}", which this router does not serve`,
      );
    }
  }
}

async function notFoundResponse(
  request: Request,
  url: URL,
  result: RouteResult,
  headers: Headers,
  deps: RouteDeps,
  fallback: () => Response | Promise<Response>,
  staticAsset: (at?: URL) => Promise<Response>,
): Promise<Response> {
  const notFoundPathname = deps.manifest.errorRoutes?.notFound;
  const notFoundTarget = notFoundPathname ? deps.manifest.dispatch[notFoundPathname] : undefined;
  if (!notFoundPathname || !notFoundTarget) return fallback();
  if (notFoundTarget.kind === "function" && !deps.functionUrls[notFoundTarget.id]) {
    return fallback();
  }

  const notFoundResult: RouteResult = {
    ...result,
    resolvedPathname: notFoundPathname,
    invocationTarget: { pathname: notFoundPathname },
  };
  const rendered = await renderDispatchTarget(
    notFoundTarget,
    request,
    url,
    notFoundResult,
    headers,
    deps,
    staticAsset,
  );
  return new Response(rendered.body, {
    status: 404,
    statusText: rendered.statusText,
    headers: rendered.headers,
  });
}

function errorRouteKind(status: number): "notFound" | "serverError" | undefined {
  if (status === 404) return "notFound";
  if (status >= 500 && status < 600) return "serverError";
  return undefined;
}

function isRenderedDocument(response: Response): boolean {
  const contentType = response.headers.get("content-type");
  if (!contentType?.startsWith("text/html")) return false;
  const contentLength = response.headers.get("content-length");
  return contentLength === null || Number(contentLength) > 0;
}

async function substituteErrorPage(
  response: Response,
  request: Request,
  url: URL,
  headers: Headers,
  manifest: NextRouteTable,
  deps: RouteDeps,
): Promise<Response> {
  const kind = errorRouteKind(response.status);
  if (!kind) return response;
  if (isFlightRequest(request.headers)) return response;
  if (isNextDataPathname(url.pathname, manifest, manifest.buildId)) return response;
  if (isRenderedDocument(response)) return response;

  const errorPathname = manifest.errorRoutes?.[kind];
  if (!errorPathname) return response;
  const errorTarget = manifest.dispatch[errorPathname];
  if (!errorTarget) return response;
  if (errorTarget.kind === "function" && !deps.functionUrls[errorTarget.id]) {
    return response;
  }

  await response.body?.cancel();
  const errorResult: RouteResult = {
    resolvedPathname: errorPathname,
    invocationTarget: { pathname: errorPathname },
  };
  const errorStaticAsset = (at: URL = new URL(errorPathname, url)) =>
    serveStaticAsset(
      request,
      at,
      deps.assetStore,
      manifest.i18n && localeOf(manifest.i18n, manifest.basePath, url),
    );
  const rendered = await renderDispatchTarget(
    errorTarget,
    request,
    url,
    errorResult,
    headers,
    deps,
    errorStaticAsset,
  );
  return new Response(rendered.body, {
    status: response.status,
    statusText: response.statusText,
    headers: rendered.headers,
  });
}

function hasUndecodableRouteMatch(
  routeMatches: Record<string, string | string[]> | undefined,
): boolean {
  if (!routeMatches) return false;
  for (const value of Object.values(routeMatches)) {
    for (const v of Array.isArray(value) ? value : [value]) {
      try {
        decodeURIComponent(v);
      } catch {
        return true;
      }
    }
  }
  return false;
}

async function prerenderResponse(
  request: Request,
  url: URL,
  result: RouteResult,
  target: PrerenderTarget,
  headers: Headers,
  deps: RouteDeps,
): Promise<Response> {
  const edgeEntryKey = target.edgeEntryKey;
  const fnUrl = edgeEntryKey || target.id === undefined ? undefined : deps.functionUrls[target.id];
  if (!fnUrl && !edgeEntryKey) return noRenderer(target.id);

  const doFetch = originFetch(deps);
  const forwardUrl = originUrl(fnUrl ?? url.origin, url, result, deps.manifest);
  const render = (rendered: Request) => {
    const entried = withEntry(rendered, target.entryKey);
    return edgeEntryKey ? edgeResponse(deps, edgeEntryKey, entried) : doFetch(entried);
  };

  if (deps.prerender) {
    return deps.prerender({
      request,
      url,
      target,
      headers,
      overriddenHeaders: middlewareOverrides(result.middleware),
      routePath: result.routePath ?? result.resolvedPathname ?? url.pathname,
      resolvedPathname: result.resolvedPathname ?? undefined,
      forwardUrl,
      forward: (rendered, rendering) => forward(forwardUrl, rendered, rendering),
      render,
    });
  }

  const answer = isSegmentPrefetch(request.headers)
    ? async (rendered: Request) => asSegmentPayload(await render(rendered))
    : render;
  return answer(forward(forwardUrl, request, headers));
}

export function invokeMiddleware(
  middleware: NonNullable<NextRouteTable["middleware"]>,
  deps: RouteDeps,
  makeRequest: () => Request,
): Promise<Response> {
  if (middleware.runtime === "nodejs") {
    const fnUrl = deps.functionUrls[middleware.id];
    if (!fnUrl) {
      throw new Error(`no function URL for middleware bundle ${middleware.id}`);
    }
    const doOrigin = originFetch(deps);
    return retryTransientOrigin(() => {
      const request = makeRequest();
      const url = new URL(request.url);
      const forwardUrl = new URL(url.pathname + encodeForwardedSearch(url.search), fnUrl);
      return doOrigin(
        withEntry(
          forward(forwardUrl, request, withoutControlHeaders(request.headers)),
          middleware.entryKey,
        ),
      );
    }, deps.isOriginThrottle).then(originAuthoredResponse);
  }

  if (!deps.edge) {
    throw new Error("no edge runtime is bound to this deployment");
  }
  return deps.edge(middleware.entryKey, makeRequest(), "middleware");
}

function noFunctionUrl(id: string): Response {
  return new Response(`No function URL for ${id}`, { status: 502 });
}

function noRenderer(id: string | undefined): Response {
  return id === undefined
    ? new Response("No renderer for prerender", { status: 502 })
    : noFunctionUrl(id);
}

function noEdgeEntry(pathname: string | null | undefined): Response {
  return new Response(`No edge entry for ${pathname}`, { status: 502 });
}

async function edgeResponse(
  deps: RouteDeps,
  entryKey: string,
  request: Request,
): Promise<Response> {
  const edge = deps.edge;
  try {
    if (!edge) throw new Error("no edge runtime is bound to this deployment");
    return await edge(entryKey, request);
  } catch (error) {
    console.error(`ocel: edge entry ${entryKey} failed`, error);
    return new Response("Edge invocation failed", { status: 500 });
  }
}

export function isRoutingRedirect(result: RouteResult): result is RouteResult & { status: number } {
  return (
    result.status !== undefined &&
    result.status >= 300 &&
    result.status < 400 &&
    (result.resolvedHeaders?.has("location") === true ||
      result.resolvedHeaders?.has("refresh") === true)
  );
}
