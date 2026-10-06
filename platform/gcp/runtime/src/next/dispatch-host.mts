import type { RoutingManifest } from "@framework/next-protocol/routing-manifest";
import { type DispatchHost, readDispatchHost } from "@framework/next-runtime/dispatch-host";
import type { Invoke } from "@framework/node-runtime/host";
import { cloudCdnRelease, newCloudCdnDispatchInvoke } from "./cloud-cdn.mjs";
import { newDiskAssetBucket, newDiskObjectStore } from "./disk-assets.mjs";
import { newInProcessImageOrigin } from "./image-origin.mjs";
import type { RefreshEndpoint } from "./refresh-endpoint.mjs";

function listFunctionIds(manifest: RoutingManifest): string[] {
  const ids = new Set<string>([manifest.entry]);
  for (const target of Object.values(manifest.dispatch)) {
    if ((target.kind === "function" || target.kind === "prerender") && target.id) {
      ids.add(target.id);
    }
  }
  if (manifest.middleware?.runtime === "nodejs") ids.add(manifest.middleware.id);
  return [...ids];
}

const staticDirVar = "OCEL_STATIC_DIR";

export function readGcpDispatchHost(env: NodeJS.ProcessEnv, localOrigin: string): DispatchHost {
  const staticDir = env[staticDirVar];
  const assetPrefix = env.OCEL_ASSET_PREFIX ?? "";
  const host = readDispatchHost(env, localOrigin, {
    originFetch: fetch,
    ...(staticDir && {
      assetBucket: newDiskAssetBucket(staticDir, assetPrefix),
      imageOrigin: newInProcessImageOrigin(newDiskObjectStore(staticDir, assetPrefix)),
    }),
  });
  return {
    ...host,
    functionUrls: Object.fromEntries(listFunctionIds(host.manifest).map((id) => [id, localOrigin])),
  };
}

export function newGcpDispatchInvoke(
  localOrigin: string,
  env: NodeJS.ProcessEnv,
  endpoint: RefreshEndpoint | undefined,
): Invoke {
  const dispatch = newCloudCdnDispatchInvoke(
    readGcpDispatchHost(env, localOrigin),
    cloudCdnRelease(env),
  );
  return async (req, res, ocel) => {
    if (endpoint && (await endpoint(req, res))) return;
    return dispatch(req, res, ocel);
  };
}
