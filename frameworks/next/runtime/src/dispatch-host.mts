import type { RoutingManifest } from "@framework/next-protocol/routing-manifest";
import { CONTROL_HEADERS, type RouteDeps, serve } from "@framework/next-router";
import type { AssetBucket } from "@framework/next-router/assets";
import { functionUrlImageOrigin } from "@framework/next-router/image";
import { fetchToNodeHandler } from "@framework/node-runtime/fetch-bridge";
import type { Invoke } from "@framework/node-runtime/host";
import { withRouterHeader } from "@framework/node-runtime/router-header";
import { uncachedResponses } from "./uncached-responses.mjs";

const NEXT_INTERNAL_PREFIX = "x-middleware-";

export function withoutClientControl(headers: Headers): Headers {
  const kept = new Headers(headers);
  for (const name of [...headers.keys()]) {
    const lower = name.toLowerCase();
    if (lower.startsWith(NEXT_INTERNAL_PREFIX) || CONTROL_HEADERS.includes(lower)) {
      kept.delete(name);
    }
  }
  return kept;
}

export interface DispatchHost {
  manifest: RoutingManifest;
  routerKind: string;
  keepCacheTags: boolean;
  localOrigin: string;
  functionUrls: Record<string, string>;
  slug: string;
  app: string;
  deploymentId: string;
  assetPrefix: string;
  assetBucket?: AssetBucket;
  imageOptimizerUrl?: string;
  originFetch: typeof fetch;
}

function newRouteDeps(
  host: DispatchHost,
  waitUntil: (promise: Promise<unknown>) => void,
): RouteDeps {
  return {
    manifest: host.manifest,
    functionUrls: {
      ...host.functionUrls,
      [host.manifest.entry]: host.localOrigin,
    },
    slug: host.slug,
    app: host.app,
    deploymentId: host.deploymentId,
    originFetch: host.originFetch,
    keepCacheTags: host.keepCacheTags,
    imageOrigin: functionUrlImageOrigin(host.imageOptimizerUrl, host.originFetch),
    assetStore: {
      store: host.assetBucket,
      assetPrefix: host.assetPrefix,
      basePath: host.manifest.basePath,
      cache: uncachedResponses(),
      waitUntil,
    },
  };
}

export async function dispatchRequest(
  request: Request,
  host: DispatchHost,
  waitUntil: (promise: Promise<unknown>) => void,
): Promise<Response> {
  const stripped = new Request(request, {
    headers: withoutClientControl(request.headers),
  });
  return withRouterHeader(await serve(stripped, newRouteDeps(host, waitUntil)), host.routerKind);
}

export function newDispatchInvoke(host: DispatchHost): Invoke {
  return (req, res, ocel) =>
    fetchToNodeHandler((request) => dispatchRequest(request, host, ocel.waitUntil))(req, res, ocel);
}
