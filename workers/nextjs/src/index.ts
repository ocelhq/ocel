import {
  resolveRoutes,
  responseToMiddlewareResult,
  type MiddlewareResult,
} from "@next/routing";
import { serveStaticAsset, type AssetStoreDeps } from "./assets";
import { createEdgeInvoker, type EdgeCacheStub, type EdgeInvoker } from "./edge";
import {
  CacheDeps,
  CacheTarget,
  admitRefresh,
  cacheKey,
  hasDraftCookie,
  refreshOutcome,
  serveCached,
  servedFromStore,
  storeInColo,
  withStatus,
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
  intercept,
  type InterceptDeps,
  type InterceptionConfig,
} from "./interception";
import { createTagClock, invalidateSnapshot, type TagClock } from "./tag-clock";
import {
  resolveDeployment,
  type DeploymentsBinding,
  type DeploymentsDeps,
} from "./deployments";
// Re-exported from the main module so ctx.exports carries a loopback binding for
// it, which is the only way the edge's dynamic worker can reach storage.
export { CacheEntrypoint } from "./cache-entrypoint";
import type { CacheEntrypointProps, IsrWriterBinding } from "./cache-entrypoint";
import { normalizeBaseDomain, previewPointer } from "./preview";
import { edgeOriginFetch } from "./signing";
import type { ObjectStoreReader } from "./tag-clock";

// The request headers a Next App Router response varies on. The colo cache key
// is derived from these directly (see variantPath), and Next's own allowHeader
// for a prerender omits them — so the origin still needs them forwarded to
// render the right variant on a cache miss.
const RSC_FORWARD_HEADERS = new Set([
  "rsc",
  "next-router-prefetch",
  "next-router-state-tree",
  "next-router-segment-prefetch",
  "next-url",
]);

// Many routes share one Lambda, so the Function URL alone does not say what to
// run: this header names which entry of that bundle the launcher must invoke.
// A manifest built before bundling carries no entryKey at all, and its
// per-route launcher ignores the header — so a *missing* entryKey means no
// header, which is not the same as an entryKey the build declared as the empty
// string. The launcher's dispatcher 502s on an absent header and looks any
// present value — "" included — up in its own entry table, so the distinction
// it draws is presence, not truthiness.
const ENTRY_HEADER = "x-ocel-entry";

// x-ocel-* is the control plane's own namespace — x-ocel-entry selects which code
// the origin runs — so no value in it may ever come from a client. The whole
// namespace is dropped from every inbound request before anything is built from
// it, which makes the forwarded values exactly the ones this worker stamped.
const CONTROL_PREFIX = "x-ocel-";

function withoutControlHeaders(headers: Headers): Headers {
  const kept = new Headers(headers);
  for (const name of [...headers.keys()]) {
    if (name.toLowerCase().startsWith(CONTROL_PREFIX)) kept.delete(name);
  }
  return kept;
}

export interface Env {
  // The service binding to the shared deployments-store worker (ADR 0002),
  // through which the active Deployment is resolved at request time.
  DEPLOYMENTS: DeploymentsBinding;
  // The project slug — addresses this project's own instance in the shared
  // deployments-store worker (idFromName), carried on every resolve RPC.
  OCEL_SLUG: string;
  // This frozen worker's own app identity — one script per app — used to look
  // up its Deployment in the project's deployments-store instance.
  OCEL_APP: string;
  // Preview mode: when OCEL_PREVIEW is "1" and a base domain is set, the worker
  // is deployed behind one exact route per preview deployment and resolves the
  // deployment pointer named by each request's subdomain instead of the default
  // one. Both must be present and well-formed; a partial config degrades to
  // normal mode.
  OCEL_PREVIEW?: string;
  OCEL_PREVIEW_BASE_DOMAIN?: string;
  // The trailing part of the preview subdomain label that is not the pointer —
  // a per-project-and-app hash keeping preview hostnames unique across the
  // zone. Stripped off the label to recover the pointer; absent means the whole
  // label is the pointer.
  OCEL_PREVIEW_LABEL_SUFFIX?: string;
  // Bound only where the edge provisioned a cache store; together with the
  // active Deployment's ISR prefix, its presence is what lets the worker
  // read the ISR cache directly.
  OCEL_CACHE_STORE?: R2Bucket;
  // The service binding to the shared ISR writer worker, which owns every
  // build's tag-clock replica: an invalidation raised on the edge is posted
  // there rather than written here, so one publisher per build merges them.
  // Optional like the store — a substrate whose bootstrap predates the writer
  // leaves an invalidation recorded in DynamoDB and unreplicated, which is what
  // it was before the edge published at all.
  ISR_WRITER?: IsrWriterBinding;
  // The edge reader's IAM credentials. The app's Lambdas are provisioned with
  // AWS_IAM Function URL auth, so the worker signs every origin forward with
  // these (SigV4). Absent only on a substrate whose edge runs inside the
  // provider's trust boundary — where the Function URLs are not IAM-gated.
  OCEL_EDGE_ACCESS_KEY_ID?: string;
  OCEL_EDGE_SECRET_KEY?: string;
  // The account-global stores the cache entrypoint addresses under those
  // credentials, and the region they live in. Nothing here is per-deployment —
  // bootstrap provisions one table and one bucket for the whole account — so
  // they ride as worker vars rather than in each Deployment record; what scopes
  // them to one app is the ISR prefix the record already carries. Optional like
  // every other binding: a substrate that binds none of them leaves the edge
  // uncached rather than failing to boot.
  OCEL_AWS_REGION?: string;
  OCEL_STATE_TABLE?: string;
  OCEL_ISR_BUCKET?: string;
  // The Function URL of the substrate's image optimizer, which /_next/image is
  // forwarded to under the same signed path as every other origin call. A worker
  // var and not a routing-manifest field: the manifest describes one build, and
  // one optimizer serves every app and deployment in the substrate. Absent on a
  // substrate that bootstrapped none, which leaves the image origin unbound and
  // every valid image request a 502.
  OCEL_IMAGE_OPTIMIZER_URL?: string;
  // The dynamic-worker loader the Deployment's edge bundle is compiled through.
  // Optional so a substrate without the binding degrades to a 500 on the edge
  // routes alone rather than failing to boot.
  LOADER?: WorkerLoader;
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
      // The bundle's identity, and the functionUrls key. Many pathnames share it.
      id: string;
      // Which entry inside that bundle renders this route.
      entryKey?: string;
      parent?: string;
      revalidate?: unknown;
    }
  | {
      kind: "prerender";
      // The parent bundle's identity, and the functionUrls key. Absent when the
      // route that regenerates this prerender runs on the edge: there is no
      // Function URL then, so there is nothing for an id to name.
      id?: string;
      // The entry inside the parent bundle that regenerates this prerender.
      entryKey?: string;
      tags?: string[];
      allowQuery?: string[];
      fallback?: {
        initialExpiration?: number;
        initialRevalidate?: number | false;
      };
      // The headers the build declares for this route's resume request, read
      // from the manifest rather than assumed.
      pprChain?: { headers: Record<string, string> };
      // Set when the route that regenerates this prerender runs on the edge:
      // its presence alone is what routes every tier below the cache to this
      // entry instead of to a Function URL — and no tier can revalidate.
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
  pathnames: string[];
  routes: unknown;
  dispatch: Record<string, DispatchTarget>;
  // Absent when the app ships no middleware.
  middleware?: { entryKey: string; matchers?: MiddlewareMatcher[] };
  // Absent when the app opted out of the built-in optimizer (a custom loader,
  // or unoptimized: true). next/image then emits the original src and never
  // requests /_next/image, so the route is not registered at all and the path
  // falls through to the asset store exactly as any other unmatched path.
  images?: ImageConfig;
  // Every static file the build emitted, by served path, with the sha256 of its
  // bytes. The image cache keys a local source by its hash, so an optimized
  // variant outlives the build it was produced under. Absent on a manifest
  // built before the adapter emitted it.
  assetHashes?: Record<string, string>;
}

// What resolveRoutes never hands back: the middleware's own Response, the
// redirect it asked for, and the request headers it rewrote. The invoker
// captures all three (see invokeMiddleware in `serve`).
export interface MiddlewareOutcome {
  response: Response;
  result: MiddlewareResult;
  headers: Headers;
}

// The relevant subset of resolveRoutes' result; typed loosely so the dispatch
// logic can be exercised with synthetic results in tests.
interface RouteResult {
  middlewareResponded?: boolean;
  status?: number;
  redirect?: { url: URL | string; status: number };
  externalRewrite?: string | URL;
  resolvedPathname?: string | null;
  invocationTarget?: { pathname: string } | null;
  // next.config `headers()` rules and the middleware's own response headers.
  resolvedHeaders?: Headers;
  middleware?: MiddlewareOutcome;
}

export interface RouteDeps {
  manifest: Manifest;
  functionUrls: Record<string, string>;
  // This worker's own naming scope (env.OCEL_SLUG / env.OCEL_APP, ADR 0005).
  // Carried here rather than read from the manifest because it identifies the
  // deployment target, not the build: the image origin loads its config from
  // image-config/<slug>/<app>/<buildId>.json.
  slug: string;
  app: string;
  // Serves this Deployment's static output (see assets.ts).
  assetStore: AssetStoreDeps;
  // Injectable so lambda/external forwarding can be observed in tests.
  fetch?: typeof fetch;

  // Present when this Deployment carries an edge bundle and the loader binding
  // exists. Absent leaves middleware and every edge route unservable — they
  // fail closed with a 500 rather than routing on without them.
  edge?: EdgeInvoker;

  // The SigV4-signing fetch used for Function-URL forwards only: the app's
  // Lambdas require AWS_IAM auth, so every origin call goes through this. Falls
  // back to `fetch` when no edge credentials are bound. Never used for external
  // rewrites or static assets — signing those would leak credentials to hosts
  // that are not the app's own Lambdas.
  originFetch?: typeof fetch;

  // Absent outside a Worker request (and in routing tests): routes then forward
  // to their origin uncached.
  cache?: CacheDeps;

  // Where a validated /_next/image request goes. The route is registered by the
  // manifest's `images` section, not by this — so on a substrate whose bootstrap
  // provisioned no optimizer nothing binds it and every valid image request is a
  // 502, in production as much as in tests.
  imageOrigin?: ImageOrigin;

  // The cache store as the durable image tier reads and writes it. Present
  // wherever the store is bound at all; unlike the ISR tier it needs no prefix
  // from the Deployment, because an optimized image is keyed by its content and
  // outlives every build.
  imageStore?: ImageStore;

  // Present when the deploy bound a cache store and injected its prefix:
  // prerender routes then read the authoritative ISR cache directly from the
  // store before falling open to the Lambda origin. Absent leaves the Lambda
  // path unchanged.
  interception?: Pick<
    InterceptDeps,
    "store" | "snapshotCache" | "now" | "waitUntil"
  > & {
    config: InterceptionConfig;
  };

  // What resolveRouteDeps resolves manifest/functionUrls/interception's ISR
  // prefix from (ADR 0002). Not itself consumed by dispatchResult — kept
  // here only as the DI seam resolveRouteDeps takes, alongside cache /
  // interception.
  deployments?: DeploymentsDeps;
}

// resolveRouteDeps resolves this app's active Deployment (ADR 0002) via
// `deployments` and wires its manifest/functionUrls/ISR prefix/asset
// prefix into a RouteDeps ready for dispatchResult — or, when there is
// nothing to serve, the terminal Response to return instead: the baked-in
// 404 when no Deployment has ever gone live for this app, or 503 when the
// store is unreachable and no cached Deployment can stand in.
export async function resolveRouteDeps(
  deployments: DeploymentsDeps,
  base: Omit<
    RouteDeps,
    | "manifest"
    | "functionUrls"
    | "interception"
    | "deployments"
    | "assetStore"
    | "edge"
    | "slug"
    | "app"
  > & {
    interception?: Pick<InterceptDeps, "store" | "snapshotCache" | "now" | "waitUntil">;
    assetStore: Omit<AssetStoreDeps, "assetPrefix">;
    // What the edge invoker is built from once the Deployment names a bundle:
    // the loader binding, the store holding it, and the cache loopback its
    // entries call back through. Both the loopback's scope and the write secret
    // its raises carry are the Deployment's own, so the stub is minted here from
    // the resolved record rather than handed over already made.
    edgeRuntime?: {
      loader: WorkerLoader;
      store: ObjectStoreReader;
      cacheEntrypoint?: (opts: { props: CacheEntrypointProps }) => EdgeCacheStub;
    };
  },
): Promise<RouteDeps | Response> {
  const resolution = await resolveDeployment(deployments);

  if (resolution.kind === "not-found") return deploymentNotFoundResponse();
  if (resolution.kind === "unavailable") return unavailableResponse();

  const { record } = resolution;
  const { edgeRuntime, ...rest } = base;
  const { edgeWorkers } = record;
  return {
    ...rest,
    slug: deployments.slug,
    app: deployments.app,
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
          )
        : undefined,
    manifest: record.routingManifest as Manifest,
    functionUrls: record.functionUrls,
    interception: base.interception && {
      ...base.interception,
      config: { isrPrefix: record.isrPrefix },
    },
    assetStore: { ...base.assetStore, assetPrefix: record.assetPrefix },
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

function unavailableResponse(): Response {
  return new Response("Service temporarily unavailable — try again shortly.", {
    status: 503,
    headers: { "content-type": "text/plain; charset=utf-8", "retry-after": "5" },
  });
}

// The image route, ahead of routing and therefore ahead of middleware — where
// Next runs it too (handleNextImageRequest is called from
// normalizeAndAttachMetadata, before handleCatchallMiddlewareRequest). Behind
// middleware, an app whose matcher is broad enough to cover /_next/image would
// have every image redirected to its login page here and served normally by
// `next start`, and would pay an edge invocation per image on the way. It also
// sits ahead of every asset fallthrough: /_next/image is in no build's static
// output, so the request would otherwise be answered with the build's 404 page.
// Registered only where the build emitted an image config.
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
    slug: deps.slug,
    app: deps.app,
    buildId: manifest.buildId,
    origin: deps.imageOrigin ?? unprovisionedImageOrigin,
    assetHashes: manifest.assetHashes,
    cache: deps.cache,
    imageStore: deps.imageStore,
  });
}

// serve is the whole request path: buffer, route, dispatch. The body is read
// here rather than at dispatch because middleware may consume it — routing gets
// a fresh stream over the buffer, and the forward that follows reuses the same
// bytes instead of a stream someone else already drained.
export async function serve(
  request: Request,
  deps: RouteDeps,
): Promise<Response> {
  const image = imageResponse(request, deps);
  if (image) return image;

  const body = await bufferBody(request);
  if (body) {
    request = new Request(request.url, {
      method: request.method,
      headers: request.headers,
      body,
      redirect: "manual",
    });
  }

  let outcome: MiddlewareOutcome | undefined;
  let failure: unknown;

  const result = (await resolveRoutes({
    url: new URL(request.url),
    buildId: deps.manifest.buildId,
    basePath: deps.manifest.basePath,
    i18n: undefined,
    headers: request.headers,
    requestBody: streamOf(body) as ReadableStream,
    pathnames: deps.manifest.pathnames,
    routes: deps.manifest.routes as Parameters<typeof resolveRoutes>[0]["routes"],

    // resolveRoutes has no matcher field: whether middleware runs at all is
    // entirely this callback's decision, and it returns neither the middleware's
    // Response nor its redirect — so both are captured on the way through.
    invokeMiddleware: async (ctx) => {
      const middleware = deps.manifest.middleware;
      if (!middleware) return {};
      try {
        if (!middlewareMatches(middleware.matchers, ctx.url, ctx.headers)) {
          return {};
        }
        if (!deps.edge) {
          throw new Error("no edge runtime is bound to this deployment");
        }
        const response = await deps.edge(
          middleware.entryKey,
          new Request(ctx.url, {
            method: request.method,
            headers: ctx.headers,
            body,
            redirect: "manual",
          }),
        );
        // ctx.headers is resolveRoutes' own mutable clone, which
        // responseToMiddlewareResult rewrites in place and never returns; hold
        // the reference or every request-header override is lost.
        const middlewareResult = responseToMiddlewareResult(
          response,
          ctx.headers,
          ctx.url,
        );
        outcome = { response, result: middlewareResult, headers: ctx.headers };
        return middlewareResult;
      } catch (error) {
        failure = error;
        return {};
      }
    },
  })) as RouteResult;

  // Fail closed: middleware that could not run must not be routed past. An auth
  // middleware failing open serves the pages it exists to protect.
  if (failure) {
    console.error("ocel: middleware invocation failed", failure);
    return new Response("Middleware failed", { status: 500 });
  }

  return dispatchResult({ ...result, middleware: outcome }, request, deps);
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
  // Applied here, after every cache tier: merging next.config `headers()` and
  // the middleware's response headers before serveCached memoizes would bake
  // one visitor's Set-Cookie into an entry every visitor is served.
  result.resolvedHeaders?.forEach((value, name) => {
    if (name.toLowerCase() !== "set-cookie") tagged.headers.set(name, value);
  });
  for (const cookie of result.resolvedHeaders?.getSetCookie() ?? []) {
    tagged.headers.append("set-cookie", cookie);
  }
  stripMiddlewareHeaders(tagged.headers);
  // x-matched-path mirrors Next.js: the matched route template with dynamic
  // segments left un-substituted (e.g. /posts/[id]). Set only when routing
  // resolved to a route — unmatched assets, 404s, and redirects carry none.
  if (result.resolvedPathname) {
    tagged.headers.set("x-matched-path", result.resolvedPathname);
  }
  return tagged;
}

// Next stamps this on a Server Action response that invalidated a tag, a cookie
// or a path. It names no tags, which is all this needs: the question it answers
// is whether the replica this worker has cached is still the current one.
const NEXT_ACTION_REVALIDATED = "x-action-revalidated";

// A Server Action reaches its origin through this worker, and the origin has
// published the new replica before it answers — so at this moment the colo's
// cached view of the tag clock is the only thing left between the visitor and
// their own write. Dropping it here is what makes an invalidation observable on
// the visitor's next request instead of up to a snapshot TTL later.
//
// Awaited rather than deferred: the guarantee being bought is that the purge has
// landed by the time the client holds the action's response, and the client's
// next request races anything left on waitUntil.
async function noteRevalidation(
  response: Response,
  deps: RouteDeps,
): Promise<void> {
  if (!deps.interception) return;
  if (!response.headers.has(NEXT_ACTION_REVALIDATED)) return;

  const { config, ...clockDeps } = deps.interception;
  await invalidateSnapshot(config, clockDeps);
}

async function dispatch(
  result: RouteResult,
  request: Request,
  deps: RouteDeps,
): Promise<Response> {
  const { manifest, functionUrls } = deps;
  const doFetch = deps.fetch ?? fetch;
  // Function-URL forwards are signed; external rewrites and static assets are
  // not (they reach arbitrary hosts, so must never carry AWS credentials).
  const doOrigin = originFetch(deps);
  const url = new URL(request.url);
  // Middleware may have rewritten the request's headers; everything downstream
  // forwards those, not the ones the client sent.
  const headers = withoutControlHeaders(
    result.middleware?.headers ?? request.headers,
  );

  if (result.middlewareResponded) {
    return middlewareResponse(result.middleware, result.status);
  }
  const middlewareRedirect = result.middleware?.result.redirect;
  if (middlewareRedirect) {
    // resolveRoutes drops a middleware redirect — it returns resolvedHeaders and
    // a status, and no resolvedPathname, so the request would otherwise fall
    // through to a 404. Response.redirect cannot carry the Set-Cookie an auth
    // middleware pairs with it, so the response is built explicitly; its
    // location comes from resolvedHeaders like every other header.
    return new Response(null, { status: middlewareRedirect.status });
  }
  if (result.redirect) {
    return Response.redirect(
      result.redirect.url.toString(),
      result.redirect.status,
    );
  }
  if (result.externalRewrite) {
    return doFetch(new Request(result.externalRewrite, request));
  }
  if (!result.resolvedPathname) {
    return serveStaticAsset(request, url, deps.assetStore);
  }

  const target = manifest.dispatch[result.resolvedPathname];
  if (!target) {
    // Not in the manifest — fall back to the asset store before giving up, so
    // any file present in static/ is still served even if unenumerated.
    return serveStaticAsset(request, url, deps.assetStore);
  }

  switch (target.kind) {
    case "static":
      // _next/static, public/, and the other truly-static files. Use the
      // ORIGINAL request/URL so the asset path matches.
      return serveStaticAsset(request, url, deps.assetStore);

    case "lambda": {
      const fnUrl = functionUrls[target.id];
      if (!fnUrl) return noFunctionUrl(target.id);
      return doOrigin(
        withEntry(
          forward(originUrl(fnUrl, url, result), request, headers),
          target.entryKey,
        ),
      );
    }

    case "prerender":
      return dispatchPrerender(request, url, result, target, headers, deps);

    case "edge": {
      if (!target.entryKey) return noEdgeEntry(result.resolvedPathname);
      // An edge route is invoked under the public origin: it renders the page a
      // browser asked for, not a forward to some other host.
      return edgeResponse(
        deps,
        target.entryKey,
        forward(originUrl(url.origin, url, result), request, headers),
      );
    }

    default:
      return serveStaticAsset(request, url, deps.assetStore);
  }
}

type PrerenderTarget = Extract<DispatchTarget, { kind: "prerender" }>;

// dispatchPrerender serves a prerendered route: from the colo cache when it can,
// from the ISR cache the worker reads itself when edge coordinates are present,
// and from the route's own renderer whenever neither can answer. That renderer
// is the parent Lambda, or — for a route that renders on the edge — the entry in
// the Deployment's edge bundle.
async function dispatchPrerender(
  request: Request,
  url: URL,
  result: RouteResult,
  target: PrerenderTarget,
  headers: Headers,
  deps: RouteDeps,
): Promise<Response> {
  // An edge-parented prerender is chosen by its edgeEntryKey, never by failing
  // to find a Function URL: a bundle id and a route id that ever collided would
  // otherwise route an edge render at a Lambda.
  const edgeEntryKey = target.edgeEntryKey;
  const fnUrl =
    edgeEntryKey || target.id === undefined
      ? undefined
      : deps.functionUrls[target.id];
  if (!fnUrl && !edgeEntryKey) return noRenderer(target.id);

  // Every Function-URL call this function makes is signed when edge credentials
  // are bound; an edge-rendered route has no Function URL at all and reaches its
  // renderer through the loader instead.
  const doFetch = originFetch(deps);
  const forwardUrl = originUrl(fnUrl ?? url.origin, url, result);
  // Every tier's origin call goes through render, under its own header set — the
  // bundle entry is stamped here so no tier can be built without it.
  const render = (rendered: Request) => {
    const entried = withEntry(rendered, target.entryKey);
    return edgeEntryKey
      ? edgeResponse(deps, edgeEntryKey, entried)
      : doFetch(entried);
  };

  if (!deps.cache) {
    return render(forward(forwardUrl, request, headers));
  }
  const cache = deps.cache;

  // A request that cannot be answered from any cache tier is forwarded as a
  // plain invocation, under its own headers. That is the only path that carries
  // cookies: allowHeader — Next's own filter for a *cached* prerender — omits
  // them, so a draft-mode request routed through the cache tiers would reach
  // the origin stripped of the very cookie that makes it draft mode, and render
  // as an ordinary visitor. A middleware that set a cookie is the same case:
  // the renderer must see it, and its response is per-visitor.
  if (
    shouldBypass(request, url, target.config) ||
    request.method !== "GET" ||
    hasDraftCookie(request) ||
    headers.has("x-middleware-set-cookie")
  ) {
    const response = await render(forward(forwardUrl, request, headers));
    return withStatus(response, "BYPASS");
  }

  const safeHeaders = new Headers();
  const allowedHeaders = target.config.allowHeader?.map((h) => h.toLowerCase());
  // Whatever middleware rewrote onto the request is part of what the route
  // renders from, so it survives allowHeader's filter.
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

  const origin = () => render(forward(forwardUrl, request, safeHeaders));

  const blockingHeaders = new Headers(safeHeaders);
  blockingHeaders.set("x-prerender-revalidate", target.config.bypassToken ?? "");
  const originBlocking = () =>
    render(forward(forwardUrl, request, blockingHeaders));

  // Edge chunks compile with no incremental cache handler, so an edge render can
  // never rewrite the ISR entry it was asked to refresh — scheduling one would
  // only burn an invocation. Edge ISR is bd ocelhq-b7l.
  const revalidates = !edgeEntryKey;

  // A pages-router data request (/_next/data/<build>/route.json) resolves to
  // the same prerender target as its html route, but must be answered with
  // JSON pageData, not html. Interception reconstructs only the html/RSC
  // variants, so those requests fall open to the Lambda exactly as today.
  const isNextData =
    url.pathname.startsWith((deps.manifest.basePath ?? "") + "/_next/data/") &&
    url.pathname.endsWith(".json");

  const routePath = result.invocationTarget?.pathname ?? url.pathname;
  const keyResult = cacheKey(
    deps.manifest.buildId,
    url.pathname,
    url,
    request.headers,
    target.config.renderingMode,
    target.allowQuery,
  );
  // A stable per-route id for deduping background revalidations, independent of
  // whether this particular variant is colo-cacheable.
  const refreshKey = `${deps.manifest.buildId}:${routePath}`;

  const cacheTarget: CacheTarget = {
    key: keyResult.cacheable ? keyResult.key : "",
    refreshKey,
    tags: target.tags,
    revalidate:
      typeof target.fallback?.initialRevalidate === "number"
        ? target.fallback.initialRevalidate
        : undefined,
    expiration: target.fallback?.initialExpiration,
  };

  // When edge cache coordinates are present, a prerender read is tried
  // directly against the cache first; any miss/expiry/error falls open to
  // the Lambda origin. A complete interception hit carries the entry's
  // remaining window so serveCached memoizes it exactly as it would the
  // Lambda's response.
  let cachingOrigin = origin;
  let tagClock: TagClock | undefined;
  // The CacheDeps every admission site here uses. It is a spread of deps.cache,
  // which is safe on purpose: the per-isolate in-flight set is a WeakMap on
  // deps.cache — the Cache object — not on the deps object, so adding the
  // tier-below read cannot fragment it.
  let cacheDeps = cache;
  if (deps.interception && !isNextData) {
    const { config, ...interceptDeps } = deps.interception;
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

    // What an admitted refresh consults before it renders. It re-reads R2 past
    // the entry memo — the memo is what declared the entry stale, and it
    // outlives the admission wait — and a fresh entry there means some other
    // colo has already regenerated this route: mirror it into the colo and
    // skip the render. Anything else (a miss, a still-stale entry, an
    // unreadable store) returns false and the render proceeds, so this can
    // only ever cost a redundant R2 GET, never a suppressed refresh.
    cacheDeps = {
      ...cache,
      refreshedFromBelow: async () => {
        const below = await intercept(request, interceptTarget, config, {
          ...interceptDeps,
          tagClock,
          freshRead: true,
        });
        if (!below) return false;
        const answered = below.kind === "complete" ? below.response : below.shell;
        if (below.stale) {
          answered.body?.cancel();
          return false;
        }
        // Fresh below — but that only settles the render when this tier ends up
        // reflecting it. A variant with no colo entry (the PPR admission site)
        // has nothing to refill: the render's whole effect would have been
        // regenerating what R2 already holds, so it is genuinely redundant.
        // A shell answering a colo-cached complete variant is neither: it
        // cannot refill that variant, so suppressing here would hold the route's
        // colo-wide claim while leaving the colo serving the entry it wanted
        // refreshed — and re-suppressing every TTL after.
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
      },
    };

    // A composed PPR response is rendered for one visitor and must not reach
    // serveCached, so a route that might postpone is read before the colo cache
    // is consulted. A STATIC route cannot postpone, so its read stays behind
    // the cache, where a hit costs no store read at all.
    const mayPostpone =
      target.config.renderingMode !== "STATIC" &&
      request.method === "GET" &&
      !hasDraftCookie(request);

    if (mayPostpone) {
      const hit = await read();
      if (hit?.kind === "ppr") {
        if (hit.stale && revalidates) {
          admitRefresh(
            cacheDeps,
            refreshKey,
            async () => {
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
      // A complete entry answered from the R2 store is a PRERENDER serve;
      // serveCached preserves that tier and memoizes the response so the next
      // request is a colo HIT. A miss falls open to the Lambda, an unstamped
      // MISS.
      if (hit?.kind !== "complete") return origin();
      // A stale entry serves immediately; the Lambda regenerates it behind the
      // request, and this write mirrors that fresh response straight into colo
      // so the next request is a colo HIT instead of another R2 round-trip.
      if (hit.stale && revalidates) {
        admitRefresh(
          cacheDeps,
          refreshKey,
          async () => {
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
    // A per-visitor dynamic variant (PPR navigation, runtime prefetch): never
    // colo-cached. It goes straight to the Lambda under the same filtered
    // headers a prerender miss uses today.
    return withStatus(await origin(), "MISS");
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

// originUrl points a request at its Function URL, preferring the routing
// result's invocation target so a rewritten path reaches the right handler.
function originUrl(fnUrl: string, url: URL, result: RouteResult): URL {
  const pathname = result.invocationTarget?.pathname ?? url.pathname;
  return new URL(pathname + url.search, fnUrl);
}

// bufferBody reads a request's body into memory so every forward of it carries a
// concrete Content-Length instead of a re-streamed (chunked) body. An AWS Lambda
// Function URL rejects a chunked request body with a 502 before the function ever
// runs — which flaps, because whether Cloudflare buffers a small body or streams
// it is nondeterministic. Buffering here is what the PPR resume already does for
// its own POST; doing it for the served request makes forwarded actions reliable.
// It is also what lets middleware read the body without starving the origin: the
// bytes outlive the one stream a Request carries.
async function bufferBody(request: Request): Promise<ArrayBuffer | null> {
  if (!request.body || request.method === "GET" || request.method === "HEAD") {
    return null;
  }
  return request.arrayBuffer();
}

function streamOf(body: ArrayBuffer | null): ReadableStream | null {
  return body === null ? null : new Blob([body]).stream();
}

// The membrane cannot answer a Function URL with a bodyless streamed response —
// AWS would never terminate it, hanging this worker's own client — so an empty
// body arrives as one sentinel byte under this header (see the membrane's
// forward). Restoring the empty body is this hop's job, and it belongs to every
// origin call rather than to one dispatch path: a prerender, a PPR resume and a
// background revalidation all forward to the same Function URLs, and a sentinel
// byte cached as page content would outlive the request that fetched it.
const EMPTY_BODY_HEADER = "x-ocel-empty-body";

// originFetch is how every Function-URL forward is made: signed when edge
// credentials are bound, and always stripped of the sentinel body.
function originFetch(deps: RouteDeps): typeof fetch {
  const doFetch = deps.originFetch ?? deps.fetch ?? fetch;
  return (async (input, init) => {
    const response = await doFetch(input as RequestInfo, init);
    if (!response.headers.has(EMPTY_BODY_HEADER)) return response;

    await response.body?.cancel();
    const empty = new Response(null, response);
    empty.headers.delete(EMPTY_BODY_HEADER);
    return empty;
  }) as typeof fetch;
}

// forward rebuilds a request against an origin URL under a chosen header set,
// keeping the method and body of the request being served.
//
// The origin sits behind a Function URL, so its `host` is that URL's host, not
// the public one the browser addressed. Next's Server Action CSRF check compares
// the `origin` header against `x-forwarded-host` (falling back to `host`), so the
// public host is stamped here — as the reverse proxy, this worker is authoritative
// for it — or every forwarded action would abort on a host/origin mismatch.
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
  return new Request(url, {
    method: request.method,
    headers: forwarded,
    body,
    redirect: "manual",
  });
}

// withEntry names the bundle entry an already-built forward must run. It wraps
// the request rather than any one header set, because a prerender forwards under
// several (raw, allowHeader-filtered, revalidating) and the origin needs the
// entry on all of them. With no entry to name the header is removed, never left
// as it was found: an entryless target's launcher ignores the header, but a
// forward carrying one it did not choose is one this worker did not author.
// Only an absent entryKey means no entry — a declared empty one is stamped as
// an empty value, matching what the manifest emits and what the launcher reads.
function withEntry(request: Request, entryKey: string | undefined): Request {
  const headers = new Headers(request.headers);
  if (entryKey !== undefined) headers.set(ENTRY_HEADER, entryKey);
  else headers.delete(ENTRY_HEADER);
  return new Request(request, { headers });
}

function noFunctionUrl(id: string): Response {
  return new Response(`No function URL for ${id}`, { status: 502 });
}

// A prerender resolves to neither a Function URL nor an edge entry: nothing can
// render it, so it fails closed like every other unresolvable target.
function noRenderer(id: string | undefined): Response {
  return id === undefined
    ? new Response("No renderer for prerender", { status: 502 })
    : noFunctionUrl(id);
}

function noEdgeEntry(pathname: string | null | undefined): Response {
  return new Response(`No edge entry for ${pathname}`, { status: 502 });
}

// edgeResponse runs one entry of the Deployment's edge bundle, fail-closed:
// a missing bundle, a loader failure or a throwing entry is a 500, never a
// silent fall-through to something else.
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

// A response with one of these statuses carries no body at all, and constructing
// one over a body — even the empty body Next's own middleware returns — throws.
const NULL_BODY_STATUSES = new Set([204, 205, 304]);

// The middleware answered the request itself. resolveRoutes reports only that it
// did — not the status, not the headers, not the body — so what goes back is the
// Response the invoker captured, minus the control headers Next reads off it.
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

// Next's middleware protocol travels on response headers — the rewrite
// destination, the request-header overrides, the set-cookie relay. They are read
// by this worker and by resolveRoutes, and must never reach the client: a
// rewrite header alone would publish the internal path every rewritten route
// resolves to.
function stripMiddlewareHeaders(headers: Headers): void {
  for (const name of [...headers.keys()]) {
    if (name.startsWith("x-middleware-")) headers.delete(name);
  }
}

// An absent or empty matcher list is Next's "run on everything" — what a bare
// middleware.ts with no `config` export produces. It never means "never run".
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

// The request headers middleware replaced, named by the response header Next's
// own server reads them from. The captured Response still carries it —
// responseToMiddlewareResult consumes it off a copy.
function middlewareOverrides(outcome: MiddlewareOutcome | undefined): Set<string> {
  const named = outcome?.response.headers.get("x-middleware-override-headers");
  return new Set(
    named
      ?.split(",")
      .map((name) => name.trim().toLowerCase())
      .filter(Boolean),
  );
}

// shouldBypass decides whether a prerender request must skip the cache and go
// straight to the origin: the route's own revalidate token, or any one of its
// bypassFor conditions. Next builds bypassFor as independent bypass *reasons*
// (server action, multipart upload, bot), so they OR — ANDing them could never
// match.
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

// matchesHas mirrors Next's own hasMatch: a bare condition matches on presence
// of a truthy value, and a condition with a value matches it as an ANCHORED
// regex — not a string equality. A repeated key is matched on its last value.
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
      // The port is not part of the host a route condition names.
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
    // Interception and static-asset serving are both enabled only where a
    // cache store is bound; the ISR prefix and asset prefix they need come
    // from the resolved Deployment below, so their config is filled in inside
    // resolveRouteDeps.
    const store = env.OCEL_CACHE_STORE;
    const originFetch = edgeOriginFetch(
      env.OCEL_EDGE_ACCESS_KEY_ID,
      env.OCEL_EDGE_SECRET_KEY,
    );

    // In preview mode the deployment pointer is named by the request's
    // subdomain; a host that yields no valid preview label has nothing to serve.
    // Preview mode is on only when OCEL_PREVIEW is "1" and a well-formed base
    // domain is configured — a missing or malformed one degrades to normal
    // serving rather than 404-ing every request.
    let pointer: string | undefined;
    const baseDomain =
      env.OCEL_PREVIEW === "1"
        ? normalizeBaseDomain(env.OCEL_PREVIEW_BASE_DOMAIN)
        : "";
    if (baseDomain) {
      const label = previewPointer(
        new URL(request.url).host,
        baseDomain,
        env.OCEL_PREVIEW_LABEL_SUFFIX,
      );
      if (label === null) return deploymentNotFoundResponse();
      pointer = label;
    }

    const deps = await resolveRouteDeps(
      { binding: env.DEPLOYMENTS, slug: env.OCEL_SLUG, app: env.OCEL_APP, pointer },
      {
        fetch,
        originFetch,
        // The optimizer is a Function URL like any app Lambda, so it is called
        // through the same signing fetch. An unsigned worker (an edge inside the
        // provider's trust boundary) forwards plainly, as it does everywhere else.
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
        },
        interception: store
          ? {
              // Passed as the binding itself: it is one stable object per
              // isolate, which is what the snapshot memo keys on.
              store,
              snapshotCache: caches.default,
              waitUntil: (promise) => ctx.waitUntil(promise),
            }
          : undefined,
        // The edge bundle lives in the same store as the ISR cache, under its
        // own prefix — never under assets/, whose keys a request pathname can
        // reach, because the bundle carries the app's edge secrets.
        edgeRuntime:
          env.LOADER && store
            ? {
                loader: env.LOADER,
                store,
                // The stub factory for this script's own CacheEntrypoint. It is
                // handed over uninvoked because only the resolved Deployment
                // knows the props to mint the stub with; the factory itself
                // never reaches a loaded worker's env, which refuses it with a
                // DataCloneError — only an invoked stub serializes.
                cacheEntrypoint: ctx.exports.CacheEntrypoint,
              }
            : undefined,
      },
    );
    if (deps instanceof Response) return deps;

    return serve(request, deps);
  },
} satisfies ExportedHandler<Env>;
