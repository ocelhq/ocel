import {
  type RouteDeps as DispatchDeps,
  serve as dispatch,
  dispatchResult as dispatchRouteResult,
  type HostRequestExtras,
  type RouteResult,
} from "@framework/next-router";
import type { AssetStoreDeps } from "@framework/next-router/assets";
import { deploymentImageOrigin, functionUrlImageOrigin } from "@framework/next-router/image";
import { buildScope, type CacheDeps } from "./cache";
import { withClientAddress } from "./client-address";
import { domainApp } from "./domains";
import { createEdgeInvoker, type EdgeCacheStub, type EdgeObjectStore, ownBundleKey } from "./edge";
import type { CacheEntrypointProps, Env } from "./env";
import { coloImageCache } from "./image";
import type { ImageStore } from "./image-store";
import { nodeOrigin } from "./node";
import { originFetchFor } from "./origin-fetch";
import { coloPrerender, type InterceptionTier } from "./prerender";
import { findPreviewTarget } from "./preview";
import { type ReleaseLookup, type ReleaseRecord, resolveRelease } from "./releases";
import { revalidationSender } from "./revalidation";
import { invalidateSnapshot } from "./tag-clock";

export { CacheEntrypoint } from "./cache-entrypoint";
export type { Env } from "./env";

const ROUTER_HEADER = "x-ocel-router";

const ROUTER_KIND = "cloudflare";

function withRouterHeader(response: Response): Response {
  if (response.headers.get(ROUTER_HEADER) === ROUTER_KIND) return response;
  const marked = new Response(response.body, response);
  marked.headers.set(ROUTER_HEADER, ROUTER_KIND);
  return marked;
}

export interface RouteDeps
  extends Omit<DispatchDeps, "prerender" | "imageCache" | "onRevalidated" | "hostRequestInit"> {
  cache?: CacheDeps;
  interception?: InterceptionTier;
  imageStore?: ImageStore;
}

function hostRequestInit(request: Request): HostRequestExtras {
  return { cf: request.cf };
}

function bound(deps: RouteDeps): DispatchDeps {
  const { cache, interception, imageStore, ...rest } = deps;
  return {
    ...rest,
    hostRequestInit,
    imageCache: coloImageCache({ slug: deps.slug, cache, imageStore }),
    prerender: cache
      ? coloPrerender({
          cache,
          interception,
          scope: buildScope(deps),
          basePath: deps.manifest.basePath,
        })
      : undefined,
    onRevalidated: interception ? () => forgetSnapshot(interception) : undefined,
  };
}

function forgetSnapshot(tier: InterceptionTier): Promise<void> {
  const { config, ...clockDeps } = tier;
  return invalidateSnapshot(config, clockDeps);
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
  | "manifest"
  | "functionUrls"
  | "interception"
  | "assetStore"
  | "edge"
  | "slug"
  | "app"
  | "appBuildId"
> & {
  interception?: Omit<InterceptionTier, "config">;
  assetStore: Omit<AssetStoreDeps, "assetPrefix">;
  imagesAtDeployment?: boolean;
  edgeRuntime?: {
    loader: WorkerLoader;
    store: EdgeObjectStore;
    cacheEntrypoint?: (opts: { props: CacheEntrypointProps }) => EdgeCacheStub;
    envelopeKey?: string;
  };
};

export type ServeFetch = (request: Request) => Promise<Response>;

interface ServeRuntime {
  serve: (record: ReleaseRecord, releases: ReleaseLookup, base: ResolveBase) => ServeFetch;
  routeDeps?: (record: ReleaseRecord, releases: ReleaseLookup, base: ResolveBase) => RouteDeps;
}

const routedRuntime: ServeRuntime = {
  serve: (record, releases, base) => {
    const deps = bound(routedDeps(record, releases, base));
    return async (request) => withRouterHeader(await dispatch(request, deps));
  },
  routeDeps: routedDeps,
};

const originRuntime: ServeRuntime = {
  serve: (record, releases, base) =>
    nodeOrigin({
      app: releases.app ?? record.app,
      functionUrls: record.functionUrls,
      originFetch: base.originFetch,
    }),
};

function runtimeFor(record: ReleaseRecord): ServeRuntime {
  return record.routeTable ? routedRuntime : originRuntime;
}

async function resolveRecord(releases: ReleaseLookup): Promise<ReleaseRecord | Response> {
  const resolution = await resolveRelease(releases);
  if (resolution.kind === "not-found") return deploymentNotFoundResponse();
  if (resolution.kind === "unavailable") return unavailableResponse();
  return resolution.record;
}

export async function resolveServe(
  releases: ReleaseLookup,
  base: ResolveBase,
): Promise<ServeFetch | Response> {
  const record = await resolveRecord(releases);
  if (record instanceof Response) return record;

  const serving = runtimeFor(record).serve(record, releases, base);
  return async (request) => withRouterHeader(await serving(request));
}

export async function resolveRouteDeps(
  releases: ReleaseLookup,
  base: ResolveBase,
): Promise<RouteDeps | Response> {
  const record = await resolveRecord(releases);
  if (record instanceof Response) return record;

  const runtime = runtimeFor(record);
  if (!runtime.routeDeps) return unroutedFrameworkResponse(record.framework);

  return runtime.routeDeps(record, releases, base);
}

function routedDeps(record: ReleaseRecord, releases: ReleaseLookup, base: ResolveBase): RouteDeps {
  const { edgeRuntime, imagesAtDeployment, ...rest } = base;
  const { edgeWorkers } = record;
  const manifest = record.routeTable?.table;
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
    slug: releases.slug,
    app,
    appBuildId: record.buildId,
    edge:
      edgeRuntime && edgeWorkers
        ? createEdgeInvoker(
            edgeRuntime.loader,
            edgeWorkers,
            edgeRuntime.store,
            edgeRuntime.cacheEntrypoint
              ? {
                  rpc: edgeRuntime.cacheEntrypoint({
                    props: { isrWriteSecret: record.isrWriteSecret, scope: record.isrPrefix },
                  }),
                  scope: record.isrPrefix,
                }
              : undefined,
            {
              env: record.env,
              envelope: record.envelope,
              envelopeKey: edgeRuntime.envelopeKey,
              releaseFingerprint: record.releaseFingerprint,
            },
          )
        : undefined,
    manifest,
    functionUrls: record.functionUrls,
    interception: base.interception && {
      ...base.interception,
      config: { isrPrefix: record.isrPrefix },
    },
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

export default {
  async fetch(request, env, ctx): Promise<Response> {
    const store = env.OCEL_CACHE_STORE;
    const originFetch = originFetchFor(env);

    const host = new URL(request.url).host;
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
      if (target === null) return deploymentNotFoundResponse();
      releases = { binding: env.RELEASES, host, slug: target.slug, label: target.label };
    }
    if (!releases.slug) return deploymentNotFoundResponse();

    const serveRequest = await resolveServe(releases, {
      fetch,
      originFetch,
      imageOrigin: functionUrlImageOrigin(env.OCEL_IMAGE_OPTIMIZER_URL, originFetch ?? fetch),
      imagesAtDeployment:
        !env.OCEL_IMAGE_OPTIMIZER_URL && env.OCEL_ORIGIN_CLIENT_CERTIFICATE !== undefined,
      imageStore: store,
      assetStore: {
        store,
        cache: caches.default,
        waitUntil: (promise) => ctx.waitUntil(promise),
      },
      cache: {
        cache: caches.default,
        waitUntil: (promise) => ctx.waitUntil(promise),
        enqueueRevalidation: revalidationSender(env),
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
              envelopeKey: env.OCEL_ENVELOPE_KEY,
            }
          : undefined,
    });
    if (serveRequest instanceof Response) return serveRequest;

    return serveRequest(withClientAddress(request));
  },
} satisfies ExportedHandler<Env>;
