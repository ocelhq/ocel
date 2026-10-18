import type {
  DispatchTarget,
  NextRouteTable,
  RoutingTable,
} from "@framework/next-protocol/route-table";
import { resolveRoutes, responseToMiddlewareResult } from "@next/routing";
import type { PurgeGroup } from "@platform/edge-contract/purge";

import {
  needsSlashNormalization,
  normalizeRepeatedSlashes,
  readPathAndQuery,
  readRawPath,
} from "@platform/edge-contract/request-path";
import type { AssetStoreDeps } from "./assets.mjs";
import { withoutClientControl, withoutControlHeaders } from "./control-headers.mjs";
import {
  applyResolvedHeaders,
  dispatch,
  forwardRequestQuery,
  invokeMiddleware,
  isRoutingRedirect,
} from "./dispatch.mjs";
import { isOnDemandRevalidate, shouldBypass } from "./has.mjs";
import { resolveLocale } from "./i18n.mjs";
import {
  type ImageCache,
  type ImageOrigin,
  isImageRequest,
  serveImage,
  unprovisionedImageOrigin,
} from "./image.mjs";
import { middlewareMatches, stripMiddlewareHeaders } from "./middleware.mjs";
import { bufferBody, streamOf } from "./origin-request.mjs";
import type { OriginThrottle } from "./retry.mjs";
import {
  dropShadowedDynamicParams,
  type MiddlewareOutcome,
  preferExactPathname,
  type RouteResult,
  repairPrefixCollidedQuery,
  withSourceInvocationTarget,
} from "./route-result.mjs";
import {
  canonicalPathname,
  isNextDataPathname,
  middlewareMatchPathname,
  middlewarePathname,
  routingPathname,
} from "./trailing-slash.mjs";

export { ENTRY_HEADER } from "./control-headers.mjs";
export { forward } from "./origin-request.mjs";
export type { MiddlewareOutcome, RouteResult };
export { isOnDemandRevalidate, shouldBypass };

type HeadersInit = NonNullable<ConstructorParameters<typeof Headers>[0]>;

export type EdgeEntryKind = "page" | "middleware";

export type EdgeInvoker = (
  entryKey: string,
  request: Request,
  kind?: EdgeEntryKind,
) => Promise<Response>;

export type PrerenderTarget = Extract<DispatchTarget, { kind: "prerender" }>;

export interface PrerenderContext {
  request: Request;
  url: URL;
  target: PrerenderTarget;
  headers: Headers;
  overriddenHeaders: Set<string>;
  routePath: string;
  resolvedPathname?: string;
  forwardUrl: URL;
  forward: (request: Request, headers: HeadersInit) => Request;
  render: (request: Request) => Promise<Response>;
}

export type Prerenderer = (context: PrerenderContext) => Promise<Response>;

export type HostRequestExtras = Omit<
  RequestInit,
  "method" | "headers" | "body" | "redirect" | "signal"
>;

export interface RouteDeps {
  manifest: NextRouteTable;
  functionUrls: Record<string, string>;
  slug: string;
  app: string;
  appBuildId: string;
  assetStore: AssetStoreDeps;
  fetch?: typeof fetch;

  edge?: EdgeInvoker;

  originFetch?: typeof fetch;

  isOriginThrottle?: OriginThrottle;

  imageOrigin?: ImageOrigin;

  imageCache?: ImageCache;

  maxImageRequestBytes?: number;

  prerender?: Prerenderer;

  hostRequestInit?: (request: Request) => HostRequestExtras;

  onPurge?: (groups: PurgeGroup[], awaited: boolean) => Promise<void>;

  keepCacheTags?: boolean;
}

function imageResponse(request: Request, deps: RouteDeps): Promise<Response> | undefined {
  const { manifest } = deps;
  if (!manifest.images) return undefined;
  const url = new URL(request.url);
  if (!isImageRequest(url.pathname, manifest.basePath)) return undefined;

  return serveImage(request, url, {
    config: manifest.images,
    basePath: manifest.basePath,
    assetPrefix: deps.assetStore.assetPrefix,
    slug: deps.slug,
    app: deps.app,
    appBuildId: deps.appBuildId,
    origin: deps.imageOrigin ?? unprovisionedImageOrigin,
    assetHashes: manifest.assetHashes,
    imageCache: deps.imageCache,
    route: (routed) => serve(routed, deps),
    maxRequestBytes: deps.maxImageRequestBytes,
  });
}

type BeforeMiddlewareRule = RoutingTable["beforeMiddleware"][number];

function isUnconditionalRedirect(route: BeforeMiddlewareRule): boolean {
  return (
    route.status !== undefined &&
    route.status >= 300 &&
    route.status < 400 &&
    route.destination === undefined &&
    !route.has &&
    !route.missing
  );
}

function nextDataMatchPathname(pathname: string, basePath: string, buildId: string): string {
  const prefix = `${basePath}/_next/data/${buildId}/`;
  if (!pathname.startsWith(prefix)) return pathname;
  const page = pathname.slice(prefix.length).replace(/\.json$/, "");
  return basePath ? `${basePath}/${page}` : `/${page}`;
}

function withoutInternalRedirects(
  table: RoutingTable,
  pathname: string,
  basePath: string,
  buildId: string,
): RoutingTable {
  const tested = table.shouldNormalizeNextData
    ? nextDataMatchPathname(pathname, basePath, buildId)
    : pathname;
  const beforeMiddleware = table.beforeMiddleware ?? [];
  const kept: BeforeMiddlewareRule[] = [];
  for (const route of beforeMiddleware) {
    if (route.priority && route.status !== undefined) continue;
    kept.push(route);
    if (isUnconditionalRedirect(route) && new RegExp(route.sourceRegex).test(tested)) {
      break;
    }
  }
  return { ...table, beforeMiddleware: kept };
}

async function trailingSlashRedirect(
  location: string,
  url: URL,
  request: Request,
  deps: RouteDeps,
): Promise<Response> {
  const table = deps.manifest.routes;
  const { resolvedHeaders } = await resolveRoutes({
    url,
    buildId: deps.manifest.buildId,
    basePath: deps.manifest.basePath,
    i18n: undefined,
    headers: request.headers,
    requestBody: streamOf(null) as ReadableStream,
    pathnames: [],
    routes: {
      ...table,
      beforeMiddleware: table.beforeMiddleware ?? [],
      beforeFiles: [],
      afterFiles: [],
      dynamicRoutes: [],
      onMatch: [],
      fallback: [],
    },
    invokeMiddleware: async () => ({}),
  });

  const headers = new Headers();
  applyResolvedHeaders(headers, resolvedHeaders);
  headers.set("location", location);
  return new Response(null, { status: 308, headers });
}

export async function serve(request: Request, deps: RouteDeps): Promise<Response> {
  if (needsSlashNormalization(readRawPath(request.url))) {
    return new Response(null, {
      status: 308,
      headers: { location: normalizeRepeatedSlashes(readPathAndQuery(request.url)) },
    });
  }

  const image = imageResponse(request, deps);
  if (image) return image;

  const url = new URL(request.url);
  const isDataRequest =
    url.pathname.endsWith("/") &&
    isNextDataPathname(url.pathname.slice(0, -1), deps.manifest, deps.manifest.buildId);
  const canonical = canonicalPathname(url.pathname, deps.manifest, isDataRequest);
  if (canonical !== url.pathname) {
    return trailingSlashRedirect(canonical + url.search, url, request, deps);
  }

  const body = await bufferBody(request);
  const requested = url.pathname;
  const routed = routingPathname(url.pathname);
  const rerouted = routed !== url.pathname;
  if (rerouted) url.pathname = routed;
  request = new Request(url, {
    ...deps.hostRequestInit?.(request),
    method: request.method,
    headers: withoutClientControl(request.headers),
    body,
    redirect: "manual",
    signal: request.signal,
  });

  const routingUrl = new URL(request.url);
  if (deps.manifest.i18n) {
    const resolution = resolveLocale(
      deps.manifest.i18n,
      deps.manifest.basePath,
      deps.manifest.buildId,
      deps.manifest.pathnames,
      routingUrl,
      request.headers,
    );
    if (resolution.redirect) {
      return Response.redirect(resolution.redirect.toString(), 307);
    }
    routingUrl.pathname = resolution.pathname;
  }

  let outcome: MiddlewareOutcome | undefined;
  let middlewareLocation: string | undefined;
  let failure: { error: unknown } | undefined;

  const result = (await resolveRoutes({
    url: routingUrl,
    buildId: deps.manifest.buildId,
    basePath: deps.manifest.basePath,
    i18n: undefined,
    headers: request.headers,
    requestBody: streamOf(body) as ReadableStream,
    pathnames: deps.manifest.pathnames,
    routes: withoutInternalRedirects(
      deps.manifest.routes,
      routingUrl.pathname,
      deps.manifest.basePath,
      deps.manifest.buildId,
    ),

    invokeMiddleware: async (ctx) => {
      const middleware = deps.manifest.middleware;
      if (!middleware) return {};
      try {
        if (isNextDataPathname(requested, deps.manifest, deps.manifest.buildId)) {
          ctx.headers.set("x-nextjs-data", "1");
        }
        const matchUrl = new URL(ctx.url);
        matchUrl.pathname = middlewareMatchPathname(
          requested,
          deps.manifest,
          deps.manifest.buildId,
        );
        if (deps.manifest.i18n) {
          matchUrl.pathname = resolveLocale(
            deps.manifest.i18n,
            deps.manifest.basePath,
            deps.manifest.buildId,
            deps.manifest.pathnames,
            matchUrl,
            ctx.headers,
          ).pathname;
        }
        if (!middlewareMatches(middleware.matchers, matchUrl, ctx.headers)) {
          return {};
        }
        const mwUrl = new URL(ctx.url);
        mwUrl.pathname = middlewarePathname(requested, deps.manifest, deps.manifest.buildId);
        if (deps.manifest.i18n) {
          mwUrl.pathname = resolveLocale(
            deps.manifest.i18n,
            deps.manifest.basePath,
            deps.manifest.buildId,
            deps.manifest.pathnames,
            mwUrl,
            ctx.headers,
          ).pathname;
        }
        const response = await invokeMiddleware(
          middleware,
          deps,
          () =>
            new Request(mwUrl, {
              method: request.method,
              headers: ctx.headers,
              body,
              redirect: "manual",
            }),
        );
        const middlewareResult = responseToMiddlewareResult(response, ctx.headers, mwUrl);
        const rewrite = middlewareResult.rewrite;
        if (rewrite && rewrite.origin === mwUrl.origin) {
          rewrite.pathname = routingPathname(rewrite.pathname);
        }
        outcome = { response, headers: ctx.headers };
        middlewareLocation = middlewareResult.responseHeaders?.get("location") ?? undefined;
        return middlewareResult;
      } catch (error) {
        failure = { error };
        return {};
      }
    },
  })) as RouteResult;

  repairPrefixCollidedQuery(result);

  if (failure) {
    console.error("ocel: middleware invocation failed", failure.error);
    return new Response("Middleware failed", { status: 500 });
  }

  return dispatchResult(
    {
      ...withSourceInvocationTarget(
        dropShadowedDynamicParams(preferExactPathname(result, deps.manifest), deps.manifest),
        routingUrl,
        deps.manifest,
      ),
      middleware: outcome,
      middlewareLocation,
    },
    request,
    deps,
  );
}

export async function dispatchResult(
  result: RouteResult,
  request: Request,
  deps: RouteDeps,
): Promise<Response> {
  const response = await dispatch(
    result,
    new Request(request, { headers: withoutControlHeaders(request.headers) }),
    deps,
  );
  if (!result.resolvedHeaders && !result.resolvedPathname) return response;

  const tagged = new Response(response.body, response);
  applyResolvedHeaders(tagged.headers, result.resolvedHeaders);
  if (isRoutingRedirect(result) && tagged.headers.get("location") !== result.middlewareLocation) {
    forwardRequestQuery(tagged.headers, request.url);
  }
  const middlewareSkip = tagged.headers.get("x-middleware-skip");
  stripMiddlewareHeaders(tagged.headers);
  if (middlewareSkip) tagged.headers.set("x-middleware-skip", middlewareSkip);
  if (result.resolvedPathname) {
    tagged.headers.set("x-matched-path", result.resolvedPathname);
  }
  return tagged;
}
