import { readFileSync } from "node:fs";
import type { RoutingManifest } from "@framework/next-protocol/routing-manifest";
import { type DispatchHost, newDispatchInvoke } from "@framework/next-runtime/dispatch-host";
import { type Invoke, invalidatesByCacheTag } from "@framework/node-runtime/host";
import { s3AssetBucket } from "./dispatch-assets.mjs";
import { credentialsOf, s3ObjectFetch, siblingOriginFetch } from "./dispatch-signing.mjs";

const routingManifestPathVar = "OCEL_ROUTING_MANIFEST";

const functionUrlsVar = "OCEL_FUNCTION_URLS";

const assetBucketVar = "OCEL_ASSET_BUCKET";

export function siblingFunctionUrls(declared: string | undefined): Record<string, string> {
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

export function readDispatchHost(env: NodeJS.ProcessEnv, localOrigin: string): DispatchHost {
  const manifestPath = env[routingManifestPathVar];
  if (!manifestPath) {
    throw new Error(`ocel: ${routingManifestPathVar} names no routing manifest`);
  }
  const manifest = JSON.parse(readFileSync(manifestPath, "utf8")) as RoutingManifest;
  const region = env.AWS_REGION;
  const bucket = env[assetBucketVar];
  if (bucket && !(region && credentialsOf(env))) {
    throw new Error(
      `ocel: ${assetBucketVar} names ${bucket} but this function has no credentials to read it with`,
    );
  }

  return {
    manifest,
    routerKind: env.OCEL_ROUTER_KIND ?? "",
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
    ...(env.OCEL_IMAGE_OPTIMIZER_URL ? { imageOptimizerUrl: env.OCEL_IMAGE_OPTIMIZER_URL } : {}),
    originFetch: siblingOriginFetch(env, region),
  };
}

export function newAwsDispatchInvoke(localOrigin: string): Invoke {
  return newDispatchInvoke(readDispatchHost(process.env, localOrigin));
}
