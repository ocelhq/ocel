import {
  dispatchResult as routeDispatchResult,
  serve as routeServe,
  type HostRequestExtras,
  type RouteDeps as RouterDeps,
  type RouteResult,
} from "@framework/next-router";
import type { AssetStoreDeps } from "@framework/next-router/assets";

import { deploymentScope, type CacheDeps } from "./cache";
import { functionUrlImageOrigin } from "@framework/next-router/image";
import { coloImageCache } from "./image";
import { coloPrerender, type InterceptionTier } from "./prerender";
import type { ImageStore } from "./image-store";
import {
  createEdgeInvoker,
  type EdgeCacheStub,
  type EdgeObjectStore,
} from "./edge";
import { invalidateSnapshot } from "./tag-clock";
import {
  resolveDeployment,
  type DeploymentRecord,
  type DeploymentsDeps,
} from "./deployments";
import type { CacheEntrypointProps, Env } from "./env";
import {
  globalPreviewTarget,
  normalizeBaseDomain,
  previewApps,
  previewTarget,
} from "./preview";
import { nodeOrigin } from "./node";
import { edgeOriginFetch } from "./signing";
import { revalidationSender } from "./revalidation";

export { CacheEntrypoint } from "./cache-entrypoint";
export type { Env } from "./env";

export const EDGE_HEADER = "x-ocel-edge";

const EDGE_KIND = "cloudflare";

export function withEdgeHeader(response: Response): Response {
  if (response.headers.get(EDGE_HEADER) === EDGE_KIND) return response;
  const marked = new Response(response.body, response);
  marked.headers.set(EDGE_HEADER, EDGE_KIND);
  return marked;
}

export interface RouteDeps
  extends Omit<
    RouterDeps,
    "prerender" | "imageCache" | "onRevalidated" | "hostRequestInit"
  > {
  cache?: CacheDeps;
  interception?: InterceptionTier;
  imageStore?: ImageStore;
}

function hostRequestInit(request: Request): HostRequestExtras {
  return { cf: request.cf };
}

function bound(deps: RouteDeps): RouterDeps {
  const { cache, interception, imageStore, ...rest } = deps;
  return {
    ...rest,
    hostRequestInit,
    imageCache: coloImageCache({ slug: deps.slug, cache, imageStore }),
    prerender: cache
      ? coloPrerender({
          cache,
          interception,
          scope: deploymentScope(deps),
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

export async function serve(
  request: Request,
  deps: RouteDeps,
): Promise<Response> {
  return withEdgeHeader(await routeServe(request, bound(deps)));
}

export function dispatchResult(
  result: RouteResult,
  request: Request,
  deps: RouteDeps,
): Promise<Response> {
  return routeDispatchResult(result, request, bound(deps));
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
  | "deploymentId"
> & {
  interception?: Omit<InterceptionTier, "config">;
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
    const deps = bound(routedDeps(record, deployments, base));
    return async (request) => withEdgeHeader(await routeServe(request, deps));
  },
  routeDeps: routedDeps,
};

const originRuntime: ServeRuntime = {
  serve: (record, deployments, base) =>
    nodeOrigin({
      app: deployments.app ?? record.app,
      functionUrls: record.functionUrls,
      originFetch: base.originFetch,
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

  const serving = runtimeFor(record).serve(record, deployments, base);
  return async (request) => withEdgeHeader(await serving(request));
}

export async function resolveRouteDeps(
  deployments: DeploymentsDeps,
  base: ResolveBase,
): Promise<RouteDeps | Response> {
  const record = await resolveRecord(deployments);
  if (record instanceof Response) return record;

  const runtime = runtimeFor(record);
  if (!runtime.routeDeps) return unroutedRuntimeResponse(record.runtime);

  return runtime.routeDeps(record, deployments, base);
}

function routedDeps(
  record: DeploymentRecord,
  deployments: DeploymentsDeps,
  base: ResolveBase,
): RouteDeps {
  const { edgeRuntime, ...rest } = base;
  const { edgeWorkers } = record;
  const manifest = record.routingManifest;
  if (!manifest) {
    throw new Error(
      `deployment ${record.deploymentId} carries no routing manifest to route with`,
    );
  }
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
      [EDGE_HEADER]: EDGE_KIND,
    },
  });
}

function unroutedRuntimeResponse(runtime: string): Response {
  return new Response(`the "${runtime}" runtime is served without edge routing.`, {
    status: 501,
    headers: {
      "content-type": "text/plain; charset=utf-8",
      [EDGE_HEADER]: EDGE_KIND,
    },
  });
}

function unavailableResponse(): Response {
  return new Response("Service temporarily unavailable — try again shortly.", {
    status: 503,
    headers: {
      "content-type": "text/plain; charset=utf-8",
      "retry-after": "5",
      [EDGE_HEADER]: EDGE_KIND,
    },
  });
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
    if (!slug) return deploymentNotFoundResponse();

    const serveRequest = await resolveServe(
      { binding: env.DEPLOYMENTS, slug, host, app, pointer },
      {
        fetch,
        originFetch,
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
