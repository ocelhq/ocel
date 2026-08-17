import {
  resolveRoutes,
  responseToMiddlewareResult,
  type I18nConfig,
} from "@next/routing";
import { isNextStaticPathname, serveStaticAsset, type AssetStoreDeps } from "./assets";
import {
  canonicalPathname,
  isNextDataPathname,
  middlewareMatchPathname,
  middlewarePathname,
  needsSlashNormalization,
  normalizeRepeatedSlashes,
  routingPathname,
  withoutBasePath,
} from "./trailing-slash";
import { localeOf, resolveLocale } from "./i18n";
import {
  createEdgeInvoker,
  type EdgeCacheStub,
  type EdgeInvoker,
  type EdgeObjectStore,
} from "./edge";
import {
  CacheDeps,
  CacheTarget,
  admitRefresh,
  asSegmentPayload,
  cacheKey,
  deploymentScope,
  hasDraftCookie,
  isSegmentPrefetch,
  refreshOutcome,
  SUPPRESS_SELF_REVALIDATION,
  serveCached,
  servedFromStore,
  storeInColo,
  withStatus,
  withVercelCacheAlias,
} from "./cache";
import {
  functionUrlImageOrigin,
  isImageRequest,
  serveImage,
  unprovisionedImageOrigin,
  type ImageConfig,
  type ImageOrigin,
} from "./image";
import type { ImageStore } from "./image-store";
import { composePpr, resumeRequest } from "./ppr";
import {
  NEXT_RENDER_RECEIPT,
  enqueued,
  revalidationSender,
  type RevalidationRoute,
} from "./revalidation";
import {
  intercept,
  type InterceptDeps,
  type InterceptionConfig,
} from "./interception";
import { createTagClock, invalidateSnapshot, type TagClock } from "./tag-clock";
import {
  resolveDeployment,
  type DeploymentRecord,
  type DeploymentsBinding,
  type DeploymentsDeps,
} from "./deployments";
export { CacheEntrypoint } from "./cache-entrypoint";
import type { CacheEntrypointProps, IsrWriterBinding } from "./cache-entrypoint";
import {
  globalPreviewTarget,
  normalizeBaseDomain,
  previewApps,
  previewTarget,
} from "./preview";
import { nodeOrigin } from "./node";
import { edgeOriginFetch } from "./signing";
import { retryTransientOrigin } from "./retry";

const RSC_FORWARD_HEADERS = new Set([
  "rsc",
  "next-router-prefetch",
  "next-router-state-tree",
  "next-router-segment-prefetch",
  "next-url",
]);

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

function isRscRequest(headers: Headers): boolean {
  return headers.get("RSC") === "1";
}

function withFlightVary(response: Response): Response {
  const tagged = new Response(response.body, response);
  tagged.headers.set("vary", FLIGHT_VARY_HEADERS.join(", "));
  return tagged;
}

const ENTRY_HEADER = "x-ocel-entry";

const PREFETCH_PURPOSE = "purpose";

const CONTROL_PREFIX = "x-ocel-";

const CONTROL_HEADERS = ["next-resume"];

function withoutControlHeaders(headers: Headers): Headers {
  const kept = new Headers(headers);
  for (const name of [...headers.keys()]) {
    const lower = name.toLowerCase();
    if (lower.startsWith(CONTROL_PREFIX) || CONTROL_HEADERS.includes(lower)) {
      kept.delete(name);
    }
  }
  return kept;
}

const NEXT_INTERNAL_HEADERS = [
  "x-middleware-rewrite",
  "x-middleware-redirect",
  "x-middleware-set-cookie",
  "x-middleware-skip",
  "x-middleware-override-headers",
  "x-middleware-next",
  "x-now-route-matches",
  "x-matched-path",
  "x-nextjs-data",
  "x-next-resume-state-length",
  "next-resume",
];

function withoutNextInternalHeaders(headers: Headers): Headers {
  const kept = new Headers(headers);
  for (const name of NEXT_INTERNAL_HEADERS) kept.delete(name);
  return kept;
}

export interface Env {
  DEPLOYMENTS: DeploymentsBinding;
  OCEL_SLUG: string;
  OCEL_APP?: string;
  OCEL_PREVIEW?: string;
  OCEL_PREVIEW_GLOBAL?: string;
  OCEL_PREVIEW_BASE_DOMAIN?: string;
  OCEL_PREVIEW_APPS?: string;
  OCEL_CACHE_STORE?: R2Bucket;
  ISR_WRITER?: IsrWriterBinding;
  OCEL_EDGE_ACCESS_KEY_ID?: string;
  OCEL_EDGE_SECRET_KEY?: string;
  OCEL_AWS_REGION?: string;
  OCEL_REVALIDATE_QUEUE_URL?: string;
  OCEL_STATE_TABLE?: string;
  OCEL_ISR_BUCKET?: string;
  OCEL_IMAGE_OPTIMIZER_URL?: string;
  OCEL_ORIGIN_BODY_LIMIT?: string;
  OCEL_ORIGIN_BODY_ENCODING?: string;
  LOADER?: WorkerLoader;
}

export type OriginBodyEncoding = "identity" | "base64";

export interface OriginBodyBudget {
  maxBytes: number;
  encoding: OriginBodyEncoding;
}

type RouteHas =
  | {
      type: "header" | "cookie" | "query";
      key: string;
      value?: string;
    }
  | {
      type: "host";
      key?: undefined;
      value: string;
    };

type DispatchTarget =
  | { kind: "static" }
  | {
      kind: "lambda";
      id: string;
      entryKey?: string;
      parent?: string;
      revalidate?: unknown;
      page?: boolean;
    }
  | {
      kind: "prerender";
      id?: string;
      entryKey?: string;
      tags?: string[];
      allowQuery?: string[];
      fallback?: {
        initialExpiration?: number;
        initialRevalidate?: number | false;
      };
      pprChain?: { headers: Record<string, string> };
      edgeEntryKey?: string;
      config: {
        allowQuery?: string[];
        allowHeader?: string[];
        bypassFor?: RouteHas[];
        renderingMode?: "STATIC" | "PARTIALLY_STATIC";
        partialFallback?: boolean;
        bypassToken?: string;
      };
    }
  | { kind: "edge"; entryKey?: string };

interface MiddlewareMatcher {
  sourceRegex: string;
  has?: RouteHas[];
  missing?: RouteHas[];
}

interface Manifest {
  buildId: string;
  basePath: string;
  trailingSlash?: boolean;
  skipTrailingSlashRedirect?: boolean;
  skipMiddlewareUrlNormalize?: boolean;
  pathnames: string[];
  routes: unknown;
  dispatch: Record<string, DispatchTarget>;
  errorRoutes?: {
    notFound?: string;
    notFoundFlight?: string;
    serverError?: string;
  };
  middleware?:
    | { runtime?: "edge"; entryKey: string; matchers?: MiddlewareMatcher[] }
    | {
        runtime: "nodejs";
        id: string;
        entryKey: string;
        matchers?: MiddlewareMatcher[];
      };
  images?: ImageConfig;
  i18n?: I18nConfig;
  assetHashes?: Record<string, string>;
  vercelCacheAlias?: boolean;
}

export interface MiddlewareOutcome {
  response: Response;
  headers: Headers;
}

interface RouteResult {
  middlewareResponded?: boolean;
  status?: number;
  redirect?: { url: URL | string; status: number };
  externalRewrite?: string | URL;
  resolvedPathname?: string | null;
  routePath?: string | null;
  invocationTarget?: {
    pathname: string;
    query?: Record<string, string | string[]>;
  } | null;
  resolvedQuery?: Record<string, string | string[]>;
  routeMatches?: Record<string, string | string[]>;
  resolvedHeaders?: Headers;
  middleware?: MiddlewareOutcome;
}

export interface RouteDeps {
  manifest: Manifest;
  functionUrls: Record<string, string>;
  slug: string;
  app: string;
  deploymentId: string;
  assetStore: AssetStoreDeps;
  fetch?: typeof fetch;

  edge?: EdgeInvoker;

  originFetch?: typeof fetch;

  originBodyBudget?: OriginBodyBudget;

  cache?: CacheDeps;

  imageOrigin?: ImageOrigin;

  imageStore?: ImageStore;

  interception?: Pick<
    InterceptDeps,
    "store" | "snapshotCache" | "now" | "waitUntil"
  > & {
    config: InterceptionConfig;
  };

  deployments?: DeploymentsDeps;
}

export type ResolveBase = Omit<
  RouteDeps,
  | "manifest"
  | "functionUrls"
  | "interception"
  | "deployments"
  | "assetStore"
  | "edge"
  | "slug"
  | "app"
  | "deploymentId"
> & {
  interception?: Pick<InterceptDeps, "store" | "snapshotCache" | "now" | "waitUntil">;
  assetStore: Omit<AssetStoreDeps, "assetPrefix">;
  edgeRuntime?: {
    loader: WorkerLoader;
    store: EdgeObjectStore;
    cacheEntrypoint?: (opts: { props: CacheEntrypointProps }) => EdgeCacheStub;
  };
};

export type ServeFetch = (request: Request) => Promise<Response>;

interface ServeRuntime {
  serve: (
    record: DeploymentRecord,
    deployments: DeploymentsDeps,
    base: ResolveBase,
  ) => ServeFetch;
  routeDeps?: (
    record: DeploymentRecord,
    deployments: DeploymentsDeps,
    base: ResolveBase,
  ) => RouteDeps;
}

const routedRuntime: ServeRuntime = {
  serve: (record, deployments, base) => {
    const deps = routedDeps(record, deployments, base);
    return (request) => serve(request, deps);
  },
  routeDeps: routedDeps,
};

const originRuntime: ServeRuntime = {
  serve: (record, deployments, base) =>
    nodeOrigin({
      app: deployments.app ?? record.app,
      functionUrls: record.functionUrls,
      originFetch: base.originFetch,
      originBodyBudget: base.originBodyBudget,
    }),
};

function runtimeFor(record: DeploymentRecord): ServeRuntime {
  return record.routingManifest ? routedRuntime : originRuntime;
}

async function resolveRecord(
  deployments: DeploymentsDeps,
): Promise<DeploymentRecord | Response> {
  const resolution = await resolveDeployment(deployments);
  if (resolution.kind === "not-found") return deploymentNotFoundResponse();
  if (resolution.kind === "unavailable") return unavailableResponse();
  return resolution.record;
}

export async function resolveServe(
  deployments: DeploymentsDeps,
  base: ResolveBase,
): Promise<ServeFetch | Response> {
  const record = await resolveRecord(deployments);
  if (record instanceof Response) return record;

  return runtimeFor(record).serve(record, deployments, base);
}

export async function resolveRouteDeps(
  deployments: DeploymentsDeps,
  base: ResolveBase,
): Promise<RouteDeps | Response> {
  const record = await resolveRecord(deployments);
  if (record instanceof Response) return record;

  const runtime = runtimeFor(record);
  if (!runtime.routeDeps) return unroutedFrameworkResponse(record.framework);

  return runtime.routeDeps(record, deployments, base);
}

function routedDeps(
  record: DeploymentRecord,
  deployments: DeploymentsDeps,
  base: ResolveBase,
): RouteDeps {
  const { edgeRuntime, ...rest } = base;
  const { edgeWorkers } = record;
  return {
    ...rest,
    slug: deployments.slug,
    app: deployments.app ?? record.app,
    deploymentId: record.deploymentId,
    edge:
      edgeRuntime && edgeWorkers
        ? createEdgeInvoker(
            edgeRuntime.loader,
            edgeWorkers,
            edgeRuntime.store,
            edgeRuntime.cacheEntrypoint
              ? {
                  rpc: edgeRuntime.cacheEntrypoint({
                    props: { isrWriteSecret: record.isrWriteSecret },
                  }),
                  scope: record.isrPrefix,
                }
              : undefined,
            {
              env: record.env,
              envelope: record.envelope,
              valueFingerprint: record.valueFingerprint,
            },
          )
        : undefined,
    manifest: record.routingManifest as Manifest,
    functionUrls: record.functionUrls,
    interception: base.interception && {
      ...base.interception,
      config: { isrPrefix: record.isrPrefix },
    },
    assetStore: {
      ...base.assetStore,
      assetPrefix: record.assetPrefix,
      basePath: (record.routingManifest as Manifest).basePath,
    },
  };
}

const DEPLOYMENT_NOT_FOUND_HTML = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Deployment not found</title></head>
<body>
<h1>No deployment yet</h1>
<p>This project has not published a deployment for this app.</p>
</body>
</html>`;

function deploymentNotFoundResponse(): Response {
  return new Response(DEPLOYMENT_NOT_FOUND_HTML, {
    status: 404,
    headers: { "content-type": "text/html; charset=utf-8" },
  });
}

function unroutedFrameworkResponse(framework: string): Response {
  return new Response(`"${framework}" is served without edge routing.`, {
    status: 501,
    headers: { "content-type": "text/plain; charset=utf-8" },
  });
}

function unavailableResponse(): Response {
  return new Response("Service temporarily unavailable — try again shortly.", {
    status: 503,
    headers: { "content-type": "text/plain; charset=utf-8", "retry-after": "5" },
  });
}

function imageResponse(
  request: Request,
  deps: RouteDeps,
): Promise<Response> | undefined {
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
    deploymentId: deps.deploymentId,
    origin: deps.imageOrigin ?? unprovisionedImageOrigin,
    assetHashes: manifest.assetHashes,
    cache: deps.cache,
    imageStore: deps.imageStore,
  });
}

type RoutingTable = Parameters<typeof resolveRoutes>[0]["routes"];

type BeforeMiddlewareRule = RoutingTable["beforeMiddleware"][number] & {
  priority?: boolean;
};

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
  routes: unknown,
  pathname: string,
  basePath: string,
  buildId: string,
): RoutingTable {
  const table = routes as RoutingTable;
  const tested = table.shouldNormalizeNextData
    ? nextDataMatchPathname(pathname, basePath, buildId)
    : pathname;
  const beforeMiddleware = (table.beforeMiddleware ?? []) as BeforeMiddlewareRule[];
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
  const table = deps.manifest.routes as RoutingTable;
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

export async function serve(
  request: Request,
  deps: RouteDeps,
): Promise<Response> {
  return withVercelCacheAlias(
    await serveRequest(request, deps),
    deps.manifest.vercelCacheAlias,
  );
}

async function serveRequest(
  request: Request,
  deps: RouteDeps,
): Promise<Response> {
  const pathAndQuery = request.url.replace(/^[a-z][a-z\d+.-]*:\/\/[^/]*/i, "");
  const queryIndex = pathAndQuery.indexOf("?");
  const rawPath = queryIndex === -1 ? pathAndQuery : pathAndQuery.slice(0, queryIndex);
  if (needsSlashNormalization(rawPath)) {
    return new Response(null, {
      status: 308,
      headers: { location: normalizeRepeatedSlashes(pathAndQuery) },
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
    method: request.method,
    headers: withoutNextInternalHeaders(request.headers),
    body,
    redirect: "manual",
    cf: request.cf,
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
        mwUrl.pathname = middlewarePathname(
          requested,
          deps.manifest,
          deps.manifest.buildId,
        );
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
        const middlewareResult = responseToMiddlewareResult(
          response,
          ctx.headers,
          mwUrl,
        );
        const rewrite = middlewareResult.rewrite;
        if (rewrite && rewrite.origin === mwUrl.origin) {
          rewrite.pathname = routingPathname(rewrite.pathname);
        }
        outcome = { response, headers: ctx.headers };
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
    },
    request,
    deps,
  );
}

function repairPrefixCollidedQuery(result: RouteResult): void {
  const routeMatches = result.routeMatches;
  if (!routeMatches) return;
  const query = result.invocationTarget?.query;
  const resolvedQuery = result.resolvedQuery;
  for (const [key, value] of Object.entries(routeMatches)) {
    if (value === undefined) continue;
    if (/^\d+$/.test(key)) continue;
    if (query && key in query) query[key] = value;
    if (resolvedQuery && key in resolvedQuery) resolvedQuery[key] = value;
  }
}

function decodedPathname(pathname: string): string | undefined {
  try {
    return decodeURIComponent(pathname);
  } catch {
    return undefined;
  }
}

function preferExactPathname(result: RouteResult, manifest: Manifest): RouteResult {
  const target = result.invocationTarget?.pathname;
  if (!result.resolvedPathname || target === undefined) return result;
  for (const candidate of [target, decodedPathname(target)]) {
    if (
      candidate !== undefined &&
      candidate !== result.resolvedPathname &&
      manifest.pathnames.includes(candidate)
    ) {
      return { ...result, resolvedPathname: candidate };
    }
  }
  return result;
}

function dropShadowedDynamicParams(result: RouteResult, manifest: Manifest): RouteResult {
  const target = result.invocationTarget;
  if (
    !target ||
    !result.routeMatches ||
    !result.resolvedPathname ||
    target.pathname !== result.resolvedPathname ||
    manifest.dispatch[result.resolvedPathname]?.kind === "prerender"
  ) {
    return result;
  }
  const query = { ...target.query };
  for (const key of Object.keys(result.routeMatches)) delete query[key];
  const { routeMatches: _routeMatches, ...rest } = result;
  return { ...rest, invocationTarget: { ...target, query } };
}

function queryFromUrl(url: URL): Record<string, string | string[]> {
  const query: Record<string, string | string[]> = {};
  for (const [key, value] of url.searchParams.entries()) {
    const existing = query[key];
    if (existing === undefined) query[key] = value;
    else query[key] = Array.isArray(existing) ? [...existing, value] : [existing, value];
  }
  return query;
}

export function ruleDestinationPathname(
  sourceRegex: string,
  destination: string,
  pathname: string,
): string | undefined {
  let match: RegExpMatchArray | null;
  try {
    match = pathname.match(new RegExp(sourceRegex));
  } catch {
    return undefined;
  }
  if (!match) return undefined;
  let out = destination.split("?")[0];
  for (let i = match.length - 1; i >= 1; i--) {
    if (match[i] !== undefined) out = out.replace(new RegExp(`\\$${i}`, "g"), match[i]);
  }
  if (match.groups) {
    const groups = Object.entries(match.groups).sort(([a], [b]) => b.length - a.length);
    for (const [key, value] of groups) {
      if (value !== undefined) out = out.replace(new RegExp(`\\$${key}`, "g"), value);
    }
  }
  return out;
}

function matchesConfigRewrite(
  pathname: string,
  resolvedPathname: string,
  routes: RoutingTable,
): boolean {
  const rules = [
    ...(routes.beforeFiles ?? []),
    ...(routes.afterFiles ?? []),
    ...(routes.fallback ?? []),
  ];
  const dynamicRoutes = routes.dynamicRoutes ?? [];
  for (const rule of rules) {
    if (!rule.destination) continue;
    const destination = ruleDestinationPathname(rule.sourceRegex, rule.destination, pathname);
    if (destination === undefined) continue;
    if (destination === resolvedPathname) return true;
    for (const dynamic of dynamicRoutes) {
      if (dynamic.destination?.split("?")[0] !== resolvedPathname) continue;
      try {
        if (new RegExp(dynamic.sourceRegex).test(destination)) return true;
      } catch {
        continue;
      }
    }
  }
  return false;
}

function withSourceInvocationTarget(
  result: RouteResult,
  routingUrl: URL,
  manifest: Manifest,
): RouteResult {
  const target = result.invocationTarget;
  const routePath = target?.pathname ?? result.resolvedPathname;
  if (!target || !result.resolvedPathname) return { ...result, routePath };
  if (target.pathname === routingUrl.pathname) return { ...result, routePath };
  if (
    !matchesConfigRewrite(
      routingUrl.pathname,
      result.resolvedPathname,
      manifest.routes as RoutingTable,
    )
  ) {
    return { ...result, routePath };
  }
  return {
    ...result,
    routePath,
    invocationTarget: {
      pathname: routingUrl.pathname,
      query: queryFromUrl(routingUrl),
    },
  };
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
  await noteRevalidation(response, deps);
  if (!result.resolvedHeaders && !result.resolvedPathname) return response;

  const tagged = new Response(response.body, response);
  applyResolvedHeaders(tagged.headers, result.resolvedHeaders);
  const middlewareSkip = tagged.headers.get("x-middleware-skip");
  stripMiddlewareHeaders(tagged.headers);
  if (middlewareSkip) tagged.headers.set("x-middleware-skip", middlewareSkip);
  if (result.resolvedPathname) {
    tagged.headers.set("x-matched-path", result.resolvedPathname);
  }
  return tagged;
}

function applyResolvedHeaders(target: Headers, resolved: Headers | undefined): void {
  if (!resolved) return;
  resolved.forEach((value, name) => {
    if (name.toLowerCase() !== "set-cookie") target.set(name, value);
  });
  for (const cookie of resolved.getSetCookie()) {
    target.append("set-cookie", cookie);
  }
}

const NEXT_ACTION_REVALIDATED = "x-action-revalidated";

async function noteRevalidation(
  response: Response,
  deps: RouteDeps,
): Promise<void> {
  if (!deps.interception) return;
  if (!response.headers.has(NEXT_ACTION_REVALIDATED)) return;

  const { config, ...clockDeps } = deps.interception;
  await invalidateSnapshot(config, clockDeps);
}

const MIDDLEWARE_PREFETCH_HEADER = "x-middleware-prefetch";

function middlewarePrefetchProbe(
  request: Request,
  pathname: string,
  manifest: Manifest,
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

async function dispatch(
  result: RouteResult,
  request: Request,
  deps: RouteDeps,
): Promise<Response> {
  const { manifest, functionUrls } = deps;
  const doFetch = deps.fetch ?? fetch;
  const doOrigin = originFetch(deps);
  const url = new URL(request.url);
  const headers = withoutControlHeaders(
    result.middleware?.headers ?? request.headers,
  );

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
    return Response.redirect(
      result.redirect.url.toString(),
      result.redirect.status,
    );
  }
  if (isRoutingRedirect(result)) {
    return new Response(null, { status: result.status });
  }
  if (result.externalRewrite) {
    return doFetch(new Request(result.externalRewrite, request));
  }
  if (!result.resolvedPathname) {
    const probe = middlewarePrefetchProbe(request, url.pathname, manifest);
    if (probe) return probe;
    const asset = await staticAsset();
    if (asset.status !== 404) return asset;
    if (isNextDataPathname(url.pathname, manifest, manifest.buildId)) return asset;
    if (isNextStaticPathname(url.pathname)) return asset;
    return notFoundResponse(request, url, result, headers, deps, () => asset, staticAsset);
  }

  if (hasUndecodableRouteMatch(result.routeMatches)) {
    return new Response("Bad Request", { status: 400 });
  }

  const target = manifest.dispatch[result.resolvedPathname];
  if (!target) {
    return notFoundResponse(request, url, result, headers, deps, staticAsset, staticAsset);
  }

  if (target.kind === "lambda") {
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
  return target.kind === "lambda" && target.page
    ? substituteErrorPage(response, request, url, headers, manifest, deps)
    : response;
}

const SERVER_ACTION_CONTENT_TYPES = [
  "application/x-www-form-urlencoded",
  "multipart/form-data",
];

function isServerAction(request: Request): boolean {
  if (request.method !== "POST") return false;
  if (request.headers.has("next-action")) return true;
  const contentType = request.headers.get("content-type") ?? "";
  const media = contentType.split(";")[0].trim().toLowerCase();
  return SERVER_ACTION_CONTENT_TYPES.includes(media);
}

function documentMethodNotAllowed(
  request: Request,
  target: DispatchTarget,
): Response | undefined {
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

    case "lambda": {
      const fnUrl = functionUrls[target.id];
      if (!fnUrl) return noFunctionUrl(target.id);
      return doOrigin(
        withEntry(
          forward(originUrl(fnUrl, url, result, manifest), request, headers),
          target.entryKey,
        ),
      );
    }

    case "prerender":
      return dispatchPrerender(request, url, result, target, headers, deps);

    case "edge": {
      if (!target.entryKey) return noEdgeEntry(result.resolvedPathname);
      return edgeResponse(
        deps,
        target.entryKey,
        forward(originUrl(url.origin, url, result, manifest), request, headers),
      );
    }

    default:
      return staticAsset();
  }
}

function notFoundRoute(
  request: Request,
  manifest: Manifest,
): string | undefined {
  const routes = manifest.errorRoutes;
  if (!routes) return undefined;
  if (isFlightRequest(request.headers) && routes.notFoundFlight) {
    return routes.notFoundFlight;
  }
  return routes.notFound;
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
  const notFoundPathname = notFoundRoute(request, deps.manifest);
  const notFoundTarget = notFoundPathname
    ? deps.manifest.dispatch[notFoundPathname]
    : undefined;
  if (!notFoundPathname || !notFoundTarget) return fallback();
  if (notFoundTarget.kind === "lambda" && !deps.functionUrls[notFoundTarget.id]) {
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
  if (!contentType || !contentType.startsWith("text/html")) return false;
  const contentLength = response.headers.get("content-length");
  return contentLength === null || Number(contentLength) > 0;
}

async function substituteErrorPage(
  response: Response,
  request: Request,
  url: URL,
  headers: Headers,
  manifest: Manifest,
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
  if (errorTarget.kind === "lambda" && !deps.functionUrls[errorTarget.id]) {
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

type PrerenderTarget = Extract<DispatchTarget, { kind: "prerender" }>;

async function dispatchPrerender(
  request: Request,
  url: URL,
  result: RouteResult,
  target: PrerenderTarget,
  headers: Headers,
  deps: RouteDeps,
): Promise<Response> {
  const edgeEntryKey = target.edgeEntryKey;
  const fnUrl =
    edgeEntryKey || target.id === undefined
      ? undefined
      : deps.functionUrls[target.id];
  if (!fnUrl && !edgeEntryKey) return noRenderer(target.id);

  const doFetch = originFetch(deps);
  const forwardUrl = originUrl(fnUrl ?? url.origin, url, result, deps.manifest);
  const render = (rendered: Request) => {
    const entried = withEntry(rendered, target.entryKey);
    return edgeEntryKey
      ? edgeResponse(deps, edgeEntryKey, entried)
      : doFetch(entried);
  };

  const segment = isSegmentPrefetch(request.headers);
  const answer = segment
    ? async (rendered: Request) => asSegmentPayload(await render(rendered))
    : render;

  if (!deps.cache) {
    return answer(forward(forwardUrl, request, headers));
  }
  const cache = deps.cache;

  if (
    shouldBypass(request, url, target.config) ||
    request.method !== "GET" ||
    hasDraftCookie(request) ||
    headers.has("x-middleware-set-cookie")
  ) {
    const response = await answer(forward(forwardUrl, request, headers));
    return withStatus(response, "BYPASS");
  }

  const safeHeaders = new Headers();
  const allowedHeaders = target.config.allowHeader?.map((h) => h.toLowerCase());
  const overridden = middlewareOverrides(result.middleware);
  for (const [name, value] of headers) {
    const lower = name.toLowerCase();
    if (
      allowedHeaders?.includes(lower) ||
      RSC_FORWARD_HEADERS.has(lower) ||
      overridden.has(lower)
    ) {
      safeHeaders.set(name, value);
    }
  }

  const isNextData =
    url.pathname.startsWith((deps.manifest.basePath ?? "") + "/_next/data/") &&
    url.pathname.endsWith(".json");

  const admissionTier =
    deps.interception && !isNextData ? deps.interception : undefined;

  const originHeaders = new Headers(safeHeaders);
  if (SUPPRESS_SELF_REVALIDATION && admissionTier) {
    originHeaders.set(PREFETCH_PURPOSE, "prefetch");
  }
  const origin = () => answer(forward(forwardUrl, request, originHeaders));

  const blockingHeaders = new Headers(safeHeaders);
  blockingHeaders.delete(PREFETCH_PURPOSE);
  blockingHeaders.set("x-prerender-revalidate", target.config.bypassToken ?? "");
  const originBlocking = () =>
    answer(forward(forwardUrl, request, blockingHeaders));

  const revalidates = !edgeEntryKey;

  const routePath = result.routePath ?? result.resolvedPathname ?? url.pathname;
  const scope = deploymentScope(deps);
  const keyResult = cacheKey(
    scope,
    url.pathname,
    url,
    request.headers,
    target.config.renderingMode,
    target.allowQuery,
  );
  const refreshKey = `${scope}:${routePath}`;

  const publicUrl = new URL(request.url);
  const revalidation: RevalidationRoute | undefined =
    admissionTier && revalidates && target.id !== undefined && routePath.startsWith("/")
      ? {
          headers: {
            "x-prerender-revalidate": target.config.bypassToken ?? "",
            ...(target.entryKey !== undefined
              ? { [ENTRY_HEADER]: target.entryKey }
              : {}),
            "x-forwarded-host": publicUrl.host,
            "x-forwarded-proto": publicUrl.protocol.replace(/:$/, ""),
          },
          expect: NEXT_RENDER_RECEIPT,
          isrPrefix: admissionTier.config.isrPrefix,
          routeId: target.id,
          routePath,
        }
      : undefined;

  const cacheTarget: CacheTarget = {
    key: keyResult.cacheable ? keyResult.key : "",
    refreshKey,
    revalidation,
    segment,
    tags: target.tags,
    revalidate:
      typeof target.fallback?.initialRevalidate === "number"
        ? target.fallback.initialRevalidate
        : undefined,
    expiration: target.fallback?.initialExpiration,
  };

  let cachingOrigin = origin;
  let tagClock: TagClock | undefined;
  let cacheDeps = cache;
  if (admissionTier) {
    const { config, ...interceptDeps } = admissionTier;
    tagClock = createTagClock(config, interceptDeps);
    const interceptTarget = {
      routePath,
      fallbackPath: result.resolvedPathname ?? undefined,
      revalidate: target.fallback?.initialRevalidate,
      expiration: target.fallback?.initialExpiration,
      tags: target.tags,
    };
    const read = once(() =>
      intercept(request, interceptTarget, config, {
        ...interceptDeps,
        tagClock,
      }),
    );

    const satisfiedFromBelow = async (refreshing: number) => {
      const below = await intercept(request, interceptTarget, config, {
        ...interceptDeps,
        tagClock,
        freshRead: true,
      });
      if (!below) return false;
      const answered = below.kind === "complete" ? below.response : below.shell;
      if (below.lastModified <= refreshing) {
        answered.body?.cancel();
        return false;
      }
      if (!keyResult.cacheable) {
        answered.body?.cancel();
        return true;
      }
      if (below.kind !== "complete") {
        answered.body?.cancel();
        return false;
      }
      await storeInColo(cacheTarget, cache, below.response);
      return true;
    };
    cacheDeps = { ...cache, satisfiedFromBelow };

    const mayPostpone =
      target.config.renderingMode !== "STATIC" &&
      request.method === "GET" &&
      !hasDraftCookie(request) &&
      !isRscRequest(request.headers);

    if (mayPostpone) {
      const hit = await read();
      if (hit?.kind === "ppr") {
        if (hit.stale && revalidates) {
          admitRefresh(
            cacheDeps,
            refreshKey,
            hit.lastModified,
            async () => {
              if (await enqueued(cacheDeps.enqueueRevalidation, revalidation, hit.lastModified)) {
                return "landed";
              }
              const response = await originBlocking();
              response.body?.cancel();
              return refreshOutcome(response);
            },
            hit.staleForMs,
          );
        }
        return composePpr(
          hit,
          render(
            resumeRequest(
              forwardUrl,
              request,
              hit.postponed,
              target.pprChain?.headers,
            ),
          ),
        );
      }
    }

    cachingOrigin = async () => {
      const hit = await read();
      if (hit?.kind !== "complete") return origin();
      if (hit.stale && revalidates) {
        admitRefresh(
          cacheDeps,
          refreshKey,
          hit.lastModified,
          async () => {
            if (await enqueued(cacheDeps.enqueueRevalidation, revalidation, hit.lastModified)) {
              return "landed";
            }
            const response = await originBlocking();
            const outcome = refreshOutcome(response);
            await storeInColo(cacheTarget, cache, response);
            return outcome;
          },
          hit.staleForMs,
        );
      }
      return servedFromStore(hit.response, hit.stale);
    };
  }

  if (!keyResult.cacheable) {
    const uncacheableHeaders = new Headers(headers);
    if (SUPPRESS_SELF_REVALIDATION && admissionTier) {
      uncacheableHeaders.set(PREFETCH_PURPOSE, "prefetch");
    }
    const response = await answer(
      forward(forwardUrl, request, uncacheableHeaders),
    );
    return withStatus(response, "MISS");
  }

  return serveCached(
    request,
    cacheTarget,
    cacheDeps,
    cachingOrigin,
    originBlocking,
    tagClock,
  );
}

function once<T>(run: () => Promise<T>): () => Promise<T> {
  let pending: Promise<T> | undefined;
  return () => (pending ??= run());
}

function dataPathname(pathname: string, url: URL, manifest: Manifest): string {
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

const REQUEST_TARGET_ILLEGAL = /[[\]^|]/g;

function encodeRequestTarget(pathname: string): string {
  return pathname.replace(
    REQUEST_TARGET_ILLEGAL,
    (character) => `%${character.charCodeAt(0).toString(16).toUpperCase()}`,
  );
}

function originUrl(
  fnUrl: string,
  url: URL,
  result: RouteResult,
  manifest: Manifest,
): URL {
  const pathname = result.invocationTarget?.pathname ?? url.pathname;
  const query = result.invocationTarget?.query;
  const search = query ? searchFromQuery(query) : url.search;
  const target = canonicalPathname(dataPathname(pathname, url, manifest), manifest);
  return new URL(encodeRequestTarget(target) + search, fnUrl);
}

async function bufferBody(request: Request): Promise<ArrayBuffer | null> {
  if (!request.body || request.method === "GET" || request.method === "HEAD") {
    return null;
  }
  return request.arrayBuffer();
}

function streamOf(body: ArrayBuffer | null): ReadableStream | null {
  return body === null ? null : new Blob([body]).stream();
}

const EMPTY_BODY_HEADER = "x-ocel-empty-body";

const NEXT_CACHE_TAGS_HEADER = "x-next-cache-tags";

const TEXT_CONTENT_TYPE =
  /^(text\/|application\/(json|javascript|xml|x-www-form-urlencoded)\b|[^;]+\+(json|xml)\b)/i;

export function originBodyBytes(
  byteLength: number,
  contentType: string | null,
  encoding: OriginBodyEncoding,
): number {
  if (encoding !== "base64") return byteLength;
  if (contentType !== null && TEXT_CONTENT_TYPE.test(contentType)) return byteLength;
  return Math.ceil(byteLength / 3) * 4;
}

export function originBodyBudget(
  maxBytes: string | undefined,
  encoding: string | undefined,
): OriginBodyBudget | undefined {
  const max = Number(maxBytes);
  if (!Number.isFinite(max) || max <= 0) return undefined;
  return { maxBytes: max, encoding: encoding === "base64" ? "base64" : "identity" };
}

function payloadTooLarge(): Response {
  return new Response(null, { status: 413 });
}

function originFetch(deps: RouteDeps): typeof fetch {
  const doFetch = deps.originFetch ?? deps.fetch ?? fetch;
  const budget = deps.originBodyBudget;
  return (async (input, init) => {
    let request: Request | undefined;
    if (budget) {
      request = new Request(input as RequestInfo, init);
      if (request.body) {
        const contentType = request.headers.get("content-type");
        const declared = Number(request.headers.get("content-length"));
        if (Number.isFinite(declared) && declared > 0) {
          if (originBodyBytes(declared, contentType, budget.encoding) > budget.maxBytes) {
            return payloadTooLarge();
          }
        } else {
          const buffered = await request.arrayBuffer();
          if (originBodyBytes(buffered.byteLength, contentType, budget.encoding) > budget.maxBytes) {
            return payloadTooLarge();
          }
          request = new Request(request, { body: buffered });
        }
      }
    }
    const response = await (request
      ? doFetch(request)
      : doFetch(input as RequestInfo, init));
    const hasEmptyBody = response.headers.has(EMPTY_BODY_HEADER);
    const hasCacheTags = response.headers.has(NEXT_CACHE_TAGS_HEADER);
    if (!hasEmptyBody && !hasCacheTags) return response;

    if (hasEmptyBody) await response.body?.cancel();
    const rebuilt = new Response(hasEmptyBody ? null : response.body, response);
    rebuilt.headers.delete(EMPTY_BODY_HEADER);
    rebuilt.headers.delete(NEXT_CACHE_TAGS_HEADER);
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
  return new Request(url, {
    method: request.method,
    headers: forwarded,
    body,
    redirect: "manual",
  });
}

function withEntry(request: Request, entryKey: string | undefined): Request {
  const headers = new Headers(request.headers);
  if (entryKey !== undefined) headers.set(ENTRY_HEADER, entryKey);
  else headers.delete(ENTRY_HEADER);
  return new Request(request, { headers });
}

const MIDDLEWARE_HEADERS_HEADER = "x-ocel-middleware-headers";

function originAuthoredResponse(response: Response): Response {
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

function invokeMiddleware(
  middleware: NonNullable<Manifest["middleware"]>,
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
      const forwardUrl = new URL(url.pathname + url.search, fnUrl);
      return doOrigin(
        withEntry(
          forward(forwardUrl, request, withoutControlHeaders(request.headers)),
          middleware.entryKey,
        ),
      );
    }).then(originAuthoredResponse);
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

function isRoutingRedirect(
  result: RouteResult,
): result is RouteResult & { status: number } {
  return (
    result.status !== undefined &&
    result.status >= 300 &&
    result.status < 400 &&
    (result.resolvedHeaders?.has("location") === true ||
      result.resolvedHeaders?.has("refresh") === true)
  );
}

const NULL_BODY_STATUSES = new Set([204, 205, 304]);

function middlewareResponse(
  outcome: MiddlewareOutcome | undefined,
  status?: number,
): Response {
  if (!outcome) return new Response(null, { status: status ?? 200 });
  const body = NULL_BODY_STATUSES.has(outcome.response.status)
    ? null
    : outcome.response.body;
  const response = new Response(body, outcome.response);
  stripMiddlewareHeaders(response.headers);
  return response;
}

function stripMiddlewareHeaders(headers: Headers): void {
  for (const name of [...headers.keys()]) {
    if (name.startsWith("x-middleware-")) headers.delete(name);
  }
}

function middlewareMatches(
  matchers: MiddlewareMatcher[] | undefined,
  url: URL,
  headers: Headers,
): boolean {
  if (!matchers || matchers.length === 0) return true;
  return matchers.some(
    (matcher) =>
      new RegExp(matcher.sourceRegex).test(url.pathname) &&
      (matcher.has ?? []).every((has) => matchesHas(has, headers, url)) &&
      !(matcher.missing ?? []).some((has) => matchesHas(has, headers, url)),
  );
}

function middlewareOverrides(outcome: MiddlewareOutcome | undefined): Set<string> {
  const named = outcome?.response.headers.get("x-middleware-override-headers");
  return new Set(
    named
      ?.split(",")
      .map((name) => name.trim().toLowerCase())
      .filter(Boolean),
  );
}

export function shouldBypass(
  request: Request,
  url: URL,
  config: { bypassFor?: RouteHas[]; bypassToken?: string },
): boolean {
  if (
    config.bypassToken &&
    request.headers.get("x-prerender-revalidate") === config.bypassToken
  ) {
    return true;
  }
  return (config.bypassFor ?? []).some((has) =>
    matchesHas(has, request.headers, url),
  );
}

function matchesHas(has: RouteHas, headers: Headers, url: URL): boolean {
  const value = hasValue(has, headers, url);
  if (!value) return false;
  if (!has.value) return true;

  const candidate = Array.isArray(value) ? value[value.length - 1] : value;
  try {
    return new RegExp(`^${has.value}$`).test(candidate);
  } catch {
    return false;
  }
}

function hasValue(
  has: RouteHas,
  headers: Headers,
  url: URL,
): string | string[] | undefined {
  switch (has.type) {
    case "header":
      return headers.get(has.key) ?? undefined;
    case "cookie":
      return cookieValue(headers.get("cookie"), has.key);
    case "query": {
      const values = url.searchParams.getAll(has.key);
      if (values.length === 0) return undefined;
      return values.length === 1 ? values[0] : values;
    }
    case "host":
      return url.host.split(":", 1)[0].toLowerCase();
  }
}

function cookieValue(header: string | null, key: string): string | undefined {
  for (const part of header?.split(";") ?? []) {
    const eq = part.indexOf("=");
    if (eq > 0 && part.slice(0, eq).trim() === key) {
      return part.slice(eq + 1).trim();
    }
  }
  return undefined;
}

export default {
  async fetch(request, env, ctx): Promise<Response> {
    const store = env.OCEL_CACHE_STORE;
    const originFetch = edgeOriginFetch(
      env.OCEL_EDGE_ACCESS_KEY_ID,
      env.OCEL_EDGE_SECRET_KEY,
    );

    const host = new URL(request.url).host;
    const global = env.OCEL_PREVIEW === "1" && env.OCEL_PREVIEW_GLOBAL === "1";

    let pointer: string | undefined;
    let app = env.OCEL_APP;
    let slug = env.OCEL_SLUG;
    const baseDomain =
      env.OCEL_PREVIEW === "1"
        ? normalizeBaseDomain(env.OCEL_PREVIEW_BASE_DOMAIN)
        : "";
    if (global) {
      const target = globalPreviewTarget(host, baseDomain);
      if (target === null) return deploymentNotFoundResponse();
      slug = target.slug;
      pointer = target.pointer;
      app = target.app;
    } else if (baseDomain) {
      const target = previewTarget(
        host,
        baseDomain,
        previewApps(env.OCEL_PREVIEW_APPS),
      );
      if (target === null) return deploymentNotFoundResponse();
      pointer = target.pointer;
      app = target.app;
    }
    if (!app && !global) return deploymentNotFoundResponse();

    const serveRequest = await resolveServe(
      { binding: env.DEPLOYMENTS, slug, host, app, pointer },
      {
        fetch,
        originFetch,
        originBodyBudget: originBodyBudget(
          env.OCEL_ORIGIN_BODY_LIMIT,
          env.OCEL_ORIGIN_BODY_ENCODING,
        ),
        imageOrigin: functionUrlImageOrigin(
          env.OCEL_IMAGE_OPTIMIZER_URL,
          originFetch ?? fetch,
        ),
        imageStore: store,
        assetStore: {
          store,
          cache: caches.default,
          waitUntil: (promise) => ctx.waitUntil(promise),
        },
        cache: {
          cache: caches.default,
          waitUntil: (promise) => ctx.waitUntil(promise),
          enqueueRevalidation: revalidationSender(
            env.OCEL_REVALIDATE_QUEUE_URL,
            env.OCEL_EDGE_ACCESS_KEY_ID,
            env.OCEL_EDGE_SECRET_KEY,
          ),
        },
        interception: store
          ? {
              store,
              snapshotCache: caches.default,
              waitUntil: (promise) => ctx.waitUntil(promise),
            }
          : undefined,
        edgeRuntime:
          env.LOADER && store
            ? {
                loader: env.LOADER,
                store,
                cacheEntrypoint: ctx.exports.CacheEntrypoint,
              }
            : undefined,
      },
    );
    if (serveRequest instanceof Response) return serveRequest;

    return serveRequest(request);
  },
} satisfies ExportedHandler<Env>;
