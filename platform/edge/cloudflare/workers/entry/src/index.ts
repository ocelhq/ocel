import type { NextRouteTable } from "@framework/next-protocol/route-table";
import {
  type RouteDeps as DispatchDeps,
  serve as dispatch,
  dispatchResult as dispatchRouteResult,
  type HostRequestExtras,
  type RouteResult,
} from "@framework/next-router";
import type { AssetStoreDeps } from "@framework/next-router/assets";
import {
  deploymentImageOrigin,
  functionUrlImageOrigin,
  originImagePath,
} from "@framework/next-router/image";
import { readReleaseToken } from "@platform/edge-contract/purge";
import { withClientAddress } from "./client-address";
import { domainApp } from "./domains";
import {
  createReleaseEdgeInvoker,
  type EdgeObjectStore,
  type EdgeRuntime,
  ownBundleKey,
} from "./edge";
import type { Env } from "./env";
import { workersCacheImage } from "./image";
import { nodeOrigin } from "./node";
import { originFetchFor } from "./origin-fetch";
import { findPreviewTarget } from "./preview";
import {
  gatePreview,
  previewGateUnreadableResponse,
  readPreviewGate,
  withResponseHeaders,
  withRobotsTag,
} from "./preview-gate";
import {
  answerPurgeOnWorker,
  isControlHost,
  isPurgeRequest,
  openInBandQueue,
  openPurgeQueue,
  respondOnControlHost,
} from "./purge-request";
import { type ReleaseLookup, type ReleaseRecord, resolveRelease } from "./releases";
import { ownRouteTableKey, readRouteTable } from "./route-table";
import { readsObjects } from "./serve";
import { type ServeFactory, serveObjects } from "./serve-objects";
import { type Serving, servePrerender } from "./serve-prerender";
import { newServing, type ServingPorts } from "./serving";

export type { Env } from "./env";
export { PurgeQueue } from "./purge-queue";
export { Serve } from "./serve";

const ROUTER_HEADER = "x-ocel-router";

const ROUTER_KIND = "cloudflare";

function withRouterHeader(response: Response): Response {
  if (response.headers.get(ROUTER_HEADER) === ROUTER_KIND) return response;
  const marked = new Response(response.body, response);
  marked.headers.set(ROUTER_HEADER, ROUTER_KIND);
  return marked;
}

export interface RouteDeps
  extends Omit<DispatchDeps, "prerender" | "imageCache" | "onPurge" | "hostRequestInit"> {
  serving?: Serving;
  imageOptimizerUrl?: string;
}

function hostRequestInit(request: Request): HostRequestExtras {
  return { cf: request.cf };
}

function bound(deps: RouteDeps): DispatchDeps {
  const { serving, imageOptimizerUrl, ...rest } = deps;
  return {
    ...rest,
    hostRequestInit,
    imageCache:
      serving && imageOptimizerUrl
        ? workersCacheImage({ serving, app: deps.app, optimizerUrl: imageOptimizerUrl })
        : undefined,
    prerender: serving ? servePrerender({ serving, app: deps.app }) : undefined,
    onPurge: serving?.purge,
  };
}

export async function serve(request: Request, deps: RouteDeps): Promise<Response> {
  return withRouterHeader(await dispatch(request, bound(deps)));
}

export function dispatchResult(
  result: RouteResult,
  request: Request,
  deps: RouteDeps,
): Promise<Response> {
  return dispatchRouteResult(result, request, bound(deps));
}

export type ResolveBase = Omit<
  RouteDeps,
  "manifest" | "functionUrls" | "serving" | "assetStore" | "edge" | "slug" | "app" | "appBuildId"
> & {
  serving?: ServingPorts;
  assetStore: Omit<AssetStoreDeps, "assetPrefix">;
  imagesAtDeployment?: boolean;
  routeTableStore?: EdgeObjectStore;
  edgeRuntime?: EdgeRuntime;
};

export type ServeFetch = (request: Request) => Promise<Response>;

interface ServedRelease {
  record: ReleaseRecord;
  routeTable?: NextRouteTable;
}

interface ServeRuntime {
  serve: (served: ServedRelease, releases: ReleaseLookup, base: ResolveBase) => ServeFetch;
  routeDeps?: (served: ServedRelease, releases: ReleaseLookup, base: ResolveBase) => RouteDeps;
}

const routedRuntime: ServeRuntime = {
  serve: (served, releases, base) => {
    const deps = bound(routedDeps(served, releases, base));
    return async (request) => withRouterHeader(await dispatch(request, deps));
  },
  routeDeps: routedDeps,
};

const originRuntime: ServeRuntime = {
  serve: ({ record }, releases, base) =>
    nodeOrigin({
      app: releases.app ?? record.app,
      functionUrls: record.functionUrls,
      originFetch: base.originFetch,
    }),
};

function runtimeFor(record: ReleaseRecord): ServeRuntime {
  return record.routeTable ? routedRuntime : originRuntime;
}

async function resolveServedRelease(
  releases: ReleaseLookup,
  base: ResolveBase,
): Promise<ServedRelease | Response> {
  const resolution = await resolveRelease(releases);
  if (resolution.kind === "not-found") return deploymentNotFoundResponse();
  if (resolution.kind === "unavailable") return unavailableResponse();
  const { record } = resolution;
  if (!record.routeTable) return { record };

  const { key } = record.routeTable;
  const refuse = (reason: unknown) => {
    console.error(
      `ocel: cannot read the route table of release ${record.release} at ${key}; answering 503`,
      reason,
    );
    return unavailableResponse();
  };
  if (!base.routeTableStore) return refuse("no cache store is bound to read it from");
  if (!ownRouteTableKey(key, releases.slug)) {
    return refuse(`the key lies outside the route tables of ${releases.slug}`);
  }
  try {
    return { record, routeTable: await readRouteTable(base.routeTableStore, key) };
  } catch (error) {
    return refuse(error);
  }
}

export async function resolveServe(
  releases: ReleaseLookup,
  base: ResolveBase,
): Promise<ServeFetch | Response> {
  const served = await resolveServedRelease(releases, base);
  if (served instanceof Response) return served;

  const serving = runtimeFor(served.record).serve(served, releases, base);
  return async (request) => withRouterHeader(await serving(request));
}

export async function resolveRouteDeps(
  releases: ReleaseLookup,
  base: ResolveBase,
): Promise<RouteDeps | Response> {
  const served = await resolveServedRelease(releases, base);
  if (served instanceof Response) return served;

  const runtime = runtimeFor(served.record);
  if (!runtime.routeDeps) return unroutedFrameworkResponse(served.record.framework);

  return runtime.routeDeps(served, releases, base);
}

function newReleaseServing(
  base: ServingPorts | undefined,
  record: ReleaseRecord,
  releases: ReleaseLookup,
  app: string,
): Serving | undefined {
  const release = readReleaseToken(record.isrPrefix);
  if (!base || release === undefined) return undefined;
  return newServing(base, {
    host: releases.host.toLowerCase(),
    app,
    release,
    slug: releases.slug,
    isrPrefix: record.isrPrefix,
  });
}

function deploymentOptimizerUrl(entryUrl: string | undefined): string | undefined {
  if (!entryUrl) return undefined;
  try {
    return new URL(originImagePath, entryUrl).href;
  } catch {
    return undefined;
  }
}

function routedDeps(
  { record, routeTable: manifest }: ServedRelease,
  releases: ReleaseLookup,
  base: ResolveBase,
): RouteDeps {
  const { edgeRuntime, imagesAtDeployment, routeTableStore, serving, ...rest } = base;
  const { edgeWorkers } = record;
  if (!manifest) {
    throw new Error(`release ${record.release} has no route table to route with`);
  }
  const app = releases.app ?? record.app;
  if (edgeWorkers && !ownBundleKey(edgeWorkers.bundleKey, releases.slug, app)) {
    throw new Error(
      `release ${record.release} of ${releases.slug}/${app} names an edge bundle outside its own prefix`,
    );
  }
  return {
    ...rest,
    imageOrigin:
      rest.imageOrigin ??
      (imagesAtDeployment
        ? deploymentImageOrigin(
            record.functionUrls[manifest.rootFunction],
            rest.originFetch ?? rest.fetch ?? fetch,
          )
        : undefined),
    imageOptimizerUrl:
      rest.imageOptimizerUrl ??
      (imagesAtDeployment
        ? deploymentOptimizerUrl(record.functionUrls[manifest.rootFunction])
        : undefined),
    slug: releases.slug,
    app,
    appBuildId: record.buildId,
    edge:
      edgeRuntime && edgeWorkers
        ? createReleaseEdgeInvoker(edgeRuntime, { ...record, edgeWorkers })
        : undefined,
    manifest,
    functionUrls: record.functionUrls,
    serving: newReleaseServing(serving, record, releases, app),
    assetStore: {
      ...base.assetStore,
      assetPrefix: record.assetPrefix,
      basePath: manifest.basePath,
      ...(record.static ? { static: record.static } : {}),
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
    headers: {
      "content-type": "text/html; charset=utf-8",
      [ROUTER_HEADER]: ROUTER_KIND,
    },
  });
}

function unroutedFrameworkResponse(framework: string): Response {
  return new Response(`the "${framework}" framework is served without edge routing.`, {
    status: 501,
    headers: {
      "content-type": "text/plain; charset=utf-8",
      [ROUTER_HEADER]: ROUTER_KIND,
    },
  });
}

function unavailableResponse(): Response {
  return new Response("Service temporarily unavailable — try again shortly.", {
    status: 503,
    headers: {
      "content-type": "text/plain; charset=utf-8",
      "retry-after": "5",
      [ROUTER_HEADER]: ROUTER_KIND,
    },
  });
}

async function gatePreviewRequest(
  request: Request,
  env: Env,
  slug: string,
): Promise<Response | { request: Request; responseHeaders: Headers }> {
  const read = readPreviewGate(env, slug);
  if (read.kind === "unreadable") return previewGateUnreadableResponse();
  if (read.kind === "open") return { request, responseHeaders: new Headers() };
  const outcome = await gatePreview(request, read.gate, Math.floor(Date.now() / 1000));
  return outcome.kind === "respond" ? outcome.response : outcome;
}

function withPreviewHeaders(response: Response, headers: Headers | undefined): Response {
  return headers ? withResponseHeaders(response, headers) : response;
}

export default {
  async fetch(request, env, ctx): Promise<Response> {
    const host = new URL(request.url).host;
    if (isControlHost(host)) {
      if (isPurgeRequest(request)) return answerPurgeOnWorker(request, env, ctx);
      if (env.OCEL_SERVES_WORKERS_DEV !== "1") return respondOnControlHost();
    }
    const serve = ctx.exports?.Serve as ServeFactory | undefined;
    const objects = serve && readsObjects(env) ? serveObjects(serve, host) : undefined;
    const originFetch = originFetchFor(env);

    let visitor: Request = request;
    let previewHeaders: Headers | undefined;
    let releases: ReleaseLookup = {
      binding: env.RELEASES,
      slug: env.OCEL_SLUG,
      host,
      app: env.OCEL_APP ?? domainApp(env.OCEL_DOMAIN_APPS, host),
    };
    if (env.OCEL_PREVIEW === "1") {
      const target = await findPreviewTarget(host, {
        baseDomain: env.OCEL_PREVIEW_BASE_DOMAIN,
        key: env.OCEL_PREVIEW_KEY,
        slug: env.OCEL_PREVIEW_GLOBAL === "1" ? undefined : env.OCEL_SLUG,
      });
      if (target === null) return withRobotsTag(deploymentNotFoundResponse());
      releases = { binding: env.RELEASES, host, slug: target.slug, label: target.label };
      const gated = await gatePreviewRequest(request, env, target.slug);
      if (gated instanceof Response) return gated;
      previewHeaders = gated.responseHeaders;
      visitor = gated.request;
    }
    if (!releases.slug) return withPreviewHeaders(deploymentNotFoundResponse(), previewHeaders);

    const serveRequest = await resolveServe(releases, {
      fetch,
      originFetch,
      imageOrigin: functionUrlImageOrigin(env.OCEL_IMAGE_OPTIMIZER_URL, originFetch ?? fetch),
      imagesAtDeployment:
        !env.OCEL_IMAGE_OPTIMIZER_URL && env.OCEL_ORIGIN_CLIENT_CERTIFICATE !== undefined,
      imageOptimizerUrl: env.OCEL_IMAGE_OPTIMIZER_URL,
      routeTableStore: objects,
      assetStore: {
        store: objects,
        cache: caches.default,
        waitUntil: (promise) => ctx.waitUntil(promise),
      },
      serving: serve
        ? {
            serve,
            queue: { raise: (mode, tags) => openInBandQueue(ctx.exports).raise(mode, tags) },
            times: { record: (mode, tags) => openPurgeQueue(ctx.exports).record(mode, tags) },
            waitUntil: (promise) => ctx.waitUntil(promise),
          }
        : undefined,
      edgeRuntime:
        env.LOADER && objects
          ? {
              loader: env.LOADER,
              store: objects,
              envelopeKey: env.OCEL_ENVELOPE_KEY,
            }
          : undefined,
    });
    if (serveRequest instanceof Response) return withPreviewHeaders(serveRequest, previewHeaders);

    return withPreviewHeaders(await serveRequest(withClientAddress(visitor)), previewHeaders);
  },
} satisfies ExportedHandler<Env>;
