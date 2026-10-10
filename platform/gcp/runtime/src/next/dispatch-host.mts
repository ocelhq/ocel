import type { NextRouteTable } from "@framework/next-protocol/route-table";
import { type DispatchHost, readDispatchHost } from "@framework/next-runtime/dispatch-host";
import type { Invoke } from "@framework/node-runtime/host";
import { fetchUndecoded } from "@framework/node-runtime/undecoded-fetch";
import { readAssetStorage } from "./asset-storage.mjs";
import { cloudCdnRelease, newCloudCdnDispatchInvoke } from "./cloud-cdn.mjs";
import { newCloudStorageAssetBucket, newCloudStorageObjectStore } from "./cloud-storage-assets.mjs";
import { newInProcessImageOrigin } from "./image-origin.mjs";
import type { RefreshEndpoint } from "./refresh-endpoint.mjs";

function listFunctionIds(manifest: NextRouteTable): string[] {
  const ids = new Set<string>([manifest.rootFunction]);
  for (const target of Object.values(manifest.dispatch)) {
    if ((target.kind === "function" || target.kind === "prerender") && target.id) {
      ids.add(target.id);
    }
  }
  if (manifest.middleware?.runtime === "nodejs") ids.add(manifest.middleware.id);
  return [...ids];
}

export function readGcpDispatchHost(env: NodeJS.ProcessEnv, localOrigin: string): DispatchHost {
  const storage = readAssetStorage(env);
  const host = readDispatchHost(env, localOrigin, {
    originFetch: fetchUndecoded,
    ...(storage && {
      assetBucket: newCloudStorageAssetBucket(storage),
      imageOrigin: newInProcessImageOrigin(newCloudStorageObjectStore(storage)),
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
