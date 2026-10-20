import { readFileSync } from "node:fs";
import type { NextRouteTable } from "@framework/next-protocol/route-table";
import { type RouteDeps, serve } from "@framework/next-router";
import type { AssetBucket, AssetStoreDeps } from "@framework/next-router/assets";
import { functionUrlImageOrigin, type ImageOrigin } from "@framework/next-router/image";
import type { OriginThrottle } from "@framework/next-router/retry";
import { fetchToNodeHandler } from "@framework/node-runtime/fetch-bridge";
import { type Invoke, invalidatesByCacheTag } from "@framework/node-runtime/host";
import { withRouterHeader } from "@framework/node-runtime/router-header";
import {
  buildIdVar,
  readStorageCoordinate,
  routerKindVar,
  routeTableVar,
  storageCoordinateVar,
} from "@platform/edge-contract/runtime-env";
import type { StorageCoordinate } from "@platform/edge-contract/storage-key";
import { uncachedResponses } from "./uncached-responses.mjs";

export interface DispatchHost {
  manifest: NextRouteTable;
  routerKind: string;
  keepCacheTags: boolean;
  localOrigin: string;
  functionUrls: Record<string, string>;
  storage: StorageCoordinate;
  appBuildId: string;
  static?: Static;
  assetBucket?: AssetBucket;
  imageOptimizerUrl?: string;
  imageOrigin?: ImageOrigin;
  maxImageRequestBytes?: number;
  originFetch: typeof fetch;
  isOriginThrottle?: OriginThrottle;
}

function newRouteDeps(
  host: DispatchHost,
  waitUntil: (promise: Promise<unknown>) => void,
): RouteDeps {
  return {
    manifest: host.manifest,
    functionUrls: {
      ...host.functionUrls,
      [host.manifest.rootFunction]: host.localOrigin,
    },
    slug: host.storage.project,
    app: host.storage.app,
    appBuildId: host.appBuildId,
    originFetch: host.originFetch,
    ...(host.isOriginThrottle ? { isOriginThrottle: host.isOriginThrottle } : {}),
    keepCacheTags: host.keepCacheTags,
    imageOrigin:
      host.imageOrigin ?? functionUrlImageOrigin(host.imageOptimizerUrl, host.originFetch),
    maxImageRequestBytes: host.maxImageRequestBytes,
    assetStore: {
      store: host.assetBucket,
      storage: host.storage,
      basePath: host.manifest.basePath,
      ...(host.static ? { static: host.static } : {}),
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
  return withRouterHeader(await serve(request, newRouteDeps(host, waitUntil)), host.routerKind);
}

export function newDispatchInvoke(host: DispatchHost): Invoke {
  return (req, res, ocel) =>
    fetchToNodeHandler((request) => dispatchRequest(request, host, ocel.waitUntil))(req, res, ocel);
}

export interface DispatchAccess {
  assetBucket?: AssetBucket;
  imageOrigin?: ImageOrigin;
  maxImageRequestBytes?: number;
  originFetch: typeof fetch;
  isOriginThrottle?: OriginThrottle;
}

const functionUrlsVar = "OCEL_FUNCTION_URLS";

const staticRulesVar = "OCEL_STATIC_RULES";

type Static = NonNullable<AssetStoreDeps["static"]>;

export function parseStaticRules(declared: string | undefined): Static | undefined {
  if (!declared) return undefined;
  const parsed = JSON.parse(declared) as Partial<Static> | null;
  if (!parsed || !Array.isArray(parsed.immutablePrefixes)) {
    throw new Error(`ocel: ${staticRulesVar} states no immutable prefixes`);
  }
  return parsed as Static;
}

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

export function readDispatchHost(
  env: NodeJS.ProcessEnv,
  localOrigin: string,
  access: DispatchAccess,
): DispatchHost {
  const routeTablePath = env[routeTableVar];
  if (!routeTablePath) {
    throw new Error(`ocel: ${routeTableVar} names no route table`);
  }
  const storage = readStorageCoordinate(env);
  if (!storage) {
    throw new Error(
      `ocel: ${storageCoordinateVar} names no release, so the dispatcher reads no static file`,
    );
  }
  const manifest = JSON.parse(readFileSync(routeTablePath, "utf8")) as NextRouteTable;

  return {
    manifest,
    routerKind: env[routerKindVar] ?? "",
    keepCacheTags: invalidatesByCacheTag(env),
    localOrigin,
    functionUrls: siblingFunctionUrls(env[functionUrlsVar]),
    storage,
    appBuildId: env[buildIdVar] ?? "",
    ...(env[staticRulesVar] ? { static: parseStaticRules(env[staticRulesVar]) } : {}),
    ...(access.assetBucket ? { assetBucket: access.assetBucket } : {}),
    ...(env.OCEL_IMAGE_OPTIMIZER_URL ? { imageOptimizerUrl: env.OCEL_IMAGE_OPTIMIZER_URL } : {}),
    ...(access.imageOrigin ? { imageOrigin: access.imageOrigin } : {}),
    maxImageRequestBytes: access.maxImageRequestBytes,
    originFetch: access.originFetch,
    ...(access.isOriginThrottle ? { isOriginThrottle: access.isOriginThrottle } : {}),
  };
}
