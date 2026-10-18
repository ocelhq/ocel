import type { NextRouteTable } from "@framework/next-protocol/route-table";
import { serve as dispatch, type HostRequestExtras, type RouteDeps } from "@framework/next-router";
import type { AssetStoreDeps } from "@framework/next-router/assets";
import { functionUrlImageOrigin, originImagePath } from "@framework/next-router/image";
import type { CompiledRouteTable } from "@platform/edge-contract/ocel-route-table";
import type { OriginImages } from "@platform/edge-contract/origin-reach";
import { readReleaseToken } from "@platform/edge-contract/purge";
import { readAssetPrefix } from "./build-assets";
import { withClientAddress } from "./client-address";
import { domainApp } from "./domains";
import {
  createReleaseEdgeInvoker,
  type EdgeObjectStore,
  type EdgeRuntime,
  ownBundleKey,
} from "./edge";
import type { Env } from "./env";
import { newPurgeQueuePort } from "./exports";
import {
  gateRequest,
  gateUnreadableResponse,
  readGate,
  withResponseHeaders,
  withRobotsTag,
} from "./gate";
import { workersCacheImage } from "./image";
import { nodeOrigin } from "./node";
import { openOrigin } from "./origin/reach";
import { findPreviewTarget } from "./preview";
import {
  answerPurgeOnWorker,
  isControlHost,
  isPurgeRequest,
  respondOnControlHost,
} from "./purge-request";
import { type ReleaseLookup, resolveRelease, type StoredRecord } from "./releases";
import { ownRouteTableKey, readRouteTable, readStoredOcelRouteTable } from "./route-table";
import { serveNodeOrigin } from "./serve-node";
import { serveObjects } from "./serve-objects";
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

function hostRequestInit(request: Request): HostRequestExtras {
  return { cf: request.cf };
}

export type ResolveBase = Pick<
  RouteDeps,
  "fetch" | "originFetch" | "imageOrigin" | "isOriginThrottle"
> & {
  images?: OriginImages;
  serving: ServingPorts;
  assetStore: Omit<AssetStoreDeps, "assetPrefix">;
  routeTableStore?: EdgeObjectStore;
  edgeRuntime: EdgeRuntime;
};

export type ServeFetch = (request: Request) => Promise<Response>;

interface OriginRelease {
  kind: "origin";
  record: StoredRecord;
  routes?: CompiledRouteTable;
}

interface RoutedRelease {
  kind: "routed";
  record: StoredRecord;
  routeTable: NextRouteTable;
  store: EdgeObjectStore;
}

type ServedRelease = OriginRelease | RoutedRelease;

function serveOrigin(
  { record, routes }: OriginRelease,
  releases: ReleaseLookup,
  base: ResolveBase,
): ServeFetch {
  const app = releases.app ?? record.app;
  const forward = nodeOrigin({
    app,
    functionUrls: record.functionUrls,
    originFetch: base.originFetch,
  });
  const serving = newReleaseServing(base.serving, record, releases, app);
  const urls = Object.values(record.functionUrls);
  if (!serving || urls.length !== 1) return forward;
  return serveNodeOrigin({ serving, app, origin: urls[0], routes, forward });
}

function serveRelease(
  served: ServedRelease,
  releases: ReleaseLookup,
  base: ResolveBase,
): ServeFetch {
  if (served.kind === "origin") return serveOrigin(served, releases, base);
  const deps = routedDeps(served, releases, base);
  return (request) => {
    const assetPrefix = readAssetPrefix(request, served.record);
    return dispatch(
      request,
      assetPrefix === deps.assetStore.assetPrefix
        ? deps
        : { ...deps, assetStore: { ...deps.assetStore, assetPrefix } },
    );
  };
}

async function resolveServedRelease(
  releases: ReleaseLookup,
  base: ResolveBase,
): Promise<ServedRelease | Response> {
  const resolution = await resolveRelease(releases);
  if (resolution.kind === "not-found") return deploymentNotFoundResponse();
  if (resolution.kind === "unavailable") return unavailableResponse();
  const { record } = resolution;
  if (!record.routeTable) return { kind: "origin", record };

  const { key } = record.routeTable;
  const refuse = (reason: unknown) => {
    console.error(
      `ocel: cannot read the route table of release ${record.release} at ${key}; answering 503`,
      reason,
    );
    return unavailableResponse();
  };
  const store = base.routeTableStore;
  if (!store) return refuse("no cache store is bound to read it from");
  if (!ownRouteTableKey(key, releases.slug)) {
    return refuse(`the key lies outside the route tables of ${releases.slug}`);
  }
  try {
    return record.routeTable.format === "ocel"
      ? { kind: "origin", record, routes: await readStoredOcelRouteTable(store, key) }
      : { kind: "routed", record, routeTable: await readRouteTable(store, key), store };
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

  const serving = serveRelease(served, releases, base);
  return async (request) => withRouterHeader(await serving(request));
}

function newReleaseServing(
  base: ServingPorts,
  record: StoredRecord,
  releases: ReleaseLookup,
  app: string,
): Serving | undefined {
  const release = readReleaseToken(record.isrPrefix);
  if (release === undefined) return undefined;
  return newServing(base, {
    host: releases.host.toLowerCase(),
    app,
    release,
    slug: releases.slug,
    isrPrefix: record.isrPrefix,
  });
}

function readImageOptimizerUrl(
  images: OriginImages | undefined,
  rootUrl: string | undefined,
): string | undefined {
  if (!images) return undefined;
  if (images.optimizerUrl !== undefined) return images.optimizerUrl;
  if (!rootUrl) return undefined;
  try {
    return new URL(originImagePath, rootUrl).href;
  } catch {
    return undefined;
  }
}

function routedDeps(
  { record, routeTable: manifest, store }: RoutedRelease,
  releases: ReleaseLookup,
  base: ResolveBase,
): RouteDeps {
  const { code } = record.edge;
  const app = releases.app ?? record.app;
  if (code && !ownBundleKey(code.bundleKey, releases.slug, app)) {
    throw new Error(
      `release ${record.release} of ${releases.slug}/${app} names an edge bundle outside its own prefix`,
    );
  }
  const rootUrl = record.functionUrls[manifest.rootFunction];
  const serving = newReleaseServing(base.serving, record, releases, app);
  const optimizerUrl = readImageOptimizerUrl(base.images, rootUrl);
  return {
    fetch: base.fetch,
    originFetch: base.originFetch,
    hostRequestInit,
    imageOrigin:
      base.imageOrigin ??
      functionUrlImageOrigin(optimizerUrl, base.originFetch ?? base.fetch ?? fetch),
    maxImageRequestBytes: base.images?.maxRequestBytes,
    imageCache:
      serving && optimizerUrl ? workersCacheImage({ serving, app, optimizerUrl }) : undefined,
    prerender: serving ? servePrerender({ serving, app }) : undefined,
    onPurge: serving?.purge,
    slug: releases.slug,
    app,
    appBuildId: record.buildId,
    edge: code ? createReleaseEdgeInvoker(base.edgeRuntime, store, record, code) : undefined,
    manifest,
    functionUrls: record.functionUrls,
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

async function passGate(
  request: Request,
  env: Env,
  slug: string,
): Promise<Response | { request: Request; responseHeaders?: Headers }> {
  const read = await readGate(env.RELEASES, slug);
  if (read.kind === "unreadable") return gateUnreadableResponse();
  if (read.kind === "open") return { request };
  const outcome = await gateRequest(request, read.gate, Math.floor(Date.now() / 1000));
  return outcome.kind === "respond" ? outcome.response : outcome;
}

function withGateHeaders(response: Response, headers: Headers | undefined): Response {
  return headers ? withResponseHeaders(response, headers) : response;
}

export default {
  async fetch(request, env, ctx): Promise<Response> {
    const host = new URL(request.url).host;
    if (isControlHost(host)) {
      if (isPurgeRequest(request)) return answerPurgeOnWorker(request, env, ctx);
      if (env.OCEL_SERVES_WORKERS_DEV !== "1") return respondOnControlHost();
    }

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
    }
    if (!releases.slug) return deploymentNotFoundResponse();

    const gated = await passGate(request, env, releases.slug);
    if (gated instanceof Response) return withRouterHeader(gated);
    const { request: visitor, responseHeaders: gateHeaders } = gated;

    const serve = ctx.exports.Serve;
    const origin = openOrigin(env);
    const objects = origin.objects ? serveObjects(serve, host) : undefined;

    const serveRequest = await resolveServe(releases, {
      fetch,
      originFetch: origin.fetch,
      isOriginThrottle: origin.isThrottle,
      images: origin.images,
      routeTableStore: objects,
      assetStore: {
        store: objects,
        cache: caches.default,
        waitUntil: (promise) => ctx.waitUntil(promise),
      },
      serving: {
        serve,
        queue: newPurgeQueuePort(ctx.exports),
        waitUntil: (promise) => ctx.waitUntil(promise),
      },
      edgeRuntime: { loader: env.LOADER, envelopeKey: env.OCEL_ENVELOPE_KEY },
    });
    if (serveRequest instanceof Response) return withGateHeaders(serveRequest, gateHeaders);

    return withGateHeaders(await serveRequest(withClientAddress(visitor)), gateHeaders);
  },
} satisfies ExportedHandler<Env>;
