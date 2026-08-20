import { readFileSync } from "node:fs";

import { serve, type RouteDeps } from "@framework/next-router";
import type { AssetBucket } from "@framework/next-router/assets";
import { functionUrlImageOrigin } from "@framework/next-router/image";
import { originBodyBudget } from "@framework/next-router/origin-body";
import type { RoutingManifest } from "@framework/next-protocol/routing-manifest";

import type { Invoke } from "../shared/membrane.mjs";
import {
  invalidatesByCacheTag,
  routingManifestPathVar,
  withEdgeHeader,
} from "../shared/edge-kind.mjs";
import { fetchToNodeHandler } from "../node/fetch-bridge.mjs";
import { s3AssetBucket, uncachedResponses } from "./router-assets.mjs";
import {
  credentialsOf,
  s3ObjectFetch,
  siblingOriginFetch,
} from "./router-signing.mjs";

const CONTROL_PREFIXES = ["x-ocel-", "x-middleware-"];

const CONTROL_HEADERS = ["next-resume"];

export function withoutClientControl(headers: Headers): Headers {
  const kept = new Headers(headers);
  for (const name of [...headers.keys()]) {
    const lower = name.toLowerCase();
    if (
      CONTROL_PREFIXES.some((prefix) => lower.startsWith(prefix)) ||
      CONTROL_HEADERS.includes(lower)
    ) {
      kept.delete(name);
    }
  }
  return kept;
}

export interface RouterHost {
  manifest: RoutingManifest;
  edgeKind: string;
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
  originBodyLimit?: string;
  originBodyEncoding?: string;
}

function routerDeps(
  host: RouterHost,
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
    originBodyBudget: originBodyBudget(
      host.originBodyLimit,
      host.originBodyEncoding,
    ),
    imageOrigin: functionUrlImageOrigin(
      host.imageOptimizerUrl,
      host.originFetch,
    ),
    assetStore: {
      store: host.assetBucket,
      assetPrefix: host.assetPrefix,
      basePath: host.manifest.basePath,
      cache: uncachedResponses(),
      waitUntil,
    },
  };
}

export async function serveRouted(
  request: Request,
  host: RouterHost,
  waitUntil: (promise: Promise<unknown>) => void,
): Promise<Response> {
  const stripped = new Request(request, {
    headers: withoutClientControl(request.headers),
  });
  return withEdgeHeader(
    await serve(stripped, routerDeps(host, waitUntil)),
    host.edgeKind,
  );
}

export function routerHostInvoke(host: RouterHost): Invoke {
  return (req, res, ocel) =>
    fetchToNodeHandler((request) => serveRouted(request, host, ocel.waitUntil))(
      req,
      res,
      ocel,
    );
}

const functionUrlsVar = "OCEL_FUNCTION_URLS";

const assetBucketVar = "OCEL_ASSET_BUCKET";

export function siblingFunctionUrls(
  declared: string | undefined,
): Record<string, string> {
  if (!declared) return {};
  const parsed: unknown = JSON.parse(declared);
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error(`ocel: ${functionUrlsVar} is not a routeId-to-URL object`);
  }
  const urls: Record<string, string> = {};
  for (const [routeId, url] of Object.entries(parsed)) {
    if (typeof url !== "string") {
      throw new Error(`ocel: ${functionUrlsVar} names no URL for ${routeId}`);
    }
    urls[routeId] = url;
  }
  return urls;
}

export function routerHostFromEnv(
  env: NodeJS.ProcessEnv,
  localOrigin: string,
): RouterHost {
  const manifestPath = env[routingManifestPathVar];
  if (!manifestPath) {
    throw new Error(`ocel: ${routingManifestPathVar} names no routing manifest`);
  }
  const manifest = JSON.parse(readFileSync(manifestPath, "utf8")) as RoutingManifest;
  const region = env.AWS_REGION;
  const bucket = env[assetBucketVar];
  if (bucket && !(region && credentialsOf(env))) {
    throw new Error(
      `ocel: ${assetBucketVar} names ${bucket} but this function holds no credentials to read it with`,
    );
  }

  return {
    manifest,
    edgeKind: env.OCEL_EDGE_KIND ?? "",
    keepCacheTags: invalidatesByCacheTag(env),
    localOrigin,
    functionUrls: siblingFunctionUrls(env[functionUrlsVar]),
    slug: env.OCEL_SLUG ?? "",
    app: env.OCEL_APP ?? manifest.appName ?? "",
    deploymentId: env.OCEL_DEPLOYMENT_ID ?? "",
    assetPrefix: env.OCEL_ASSET_PREFIX ?? "",
    ...(bucket && region
      ? { assetBucket: s3AssetBucket(bucket, region, s3ObjectFetch(env, region)) }
      : {}),
    ...(env.OCEL_IMAGE_OPTIMIZER_URL
      ? { imageOptimizerUrl: env.OCEL_IMAGE_OPTIMIZER_URL }
      : {}),
    originFetch: siblingOriginFetch(env, region),
    ...(env.OCEL_ORIGIN_BODY_LIMIT
      ? { originBodyLimit: env.OCEL_ORIGIN_BODY_LIMIT }
      : {}),
    ...(env.OCEL_ORIGIN_BODY_ENCODING
      ? { originBodyEncoding: env.OCEL_ORIGIN_BODY_ENCODING }
      : {}),
  };
}
