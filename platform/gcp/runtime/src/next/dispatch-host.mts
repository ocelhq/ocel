import { newInProcessImageOrigin } from "@framework/next-image-optimizer/in-process-origin";
import type { NextRouteTable } from "@framework/next-protocol/route-table";
import {
  type DispatchHost,
  newDispatchInvoke,
  readDispatchHost,
} from "@framework/next-runtime/dispatch-host";
import type { Invoke } from "@framework/node-runtime/host";
import { fetchUndecoded } from "@framework/node-runtime/undecoded-fetch";
import { readEdgeCacheMode } from "@platform/edge-contract/edge-cache";
import { cloudCdnRelease } from "../cache/cloud-cdn-tags.mjs";
import { readAssetStorage } from "./asset-storage.mjs";
import { newCloudCdnDispatchInvoke } from "./cloud-cdn.mjs";
import { isCloudRunThrottle } from "./cloud-run-throttle.mjs";
import { newCloudStorageAssetBucket, newCloudStorageObjectStore } from "./cloud-storage-assets.mjs";

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
    isOriginThrottle: isCloudRunThrottle,
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

export function newGcpDispatchInvoke(localOrigin: string, env: NodeJS.ProcessEnv): Invoke {
  const host = readGcpDispatchHost(env, localOrigin);
  if (readEdgeCacheMode(env) === undefined) return newDispatchInvoke(host);
  return newCloudCdnDispatchInvoke(host, cloudCdnRelease(env));
}
