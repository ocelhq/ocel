import type { RoutingManifest } from "@framework/next-protocol/routing-manifest";
import {
  type DispatchHost,
  newDispatchInvoke,
  readDispatchHost,
} from "@framework/next-runtime/dispatch-host";
import type { Invoke } from "@framework/node-runtime/host";
import { diskAssetBucket, diskObjectStore } from "./disk-assets.mjs";
import { inProcessImageOrigin } from "./image-origin.mjs";

function functionIds(manifest: RoutingManifest): string[] {
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
      assetBucket: diskAssetBucket(staticDir, assetPrefix),
      imageOrigin: inProcessImageOrigin(diskObjectStore(staticDir, assetPrefix)),
    }),
  });
  return {
    ...host,
    functionUrls: Object.fromEntries(functionIds(host.manifest).map((id) => [id, localOrigin])),
  };
}

export function newGcpDispatchInvoke(localOrigin: string): Invoke {
  return newDispatchInvoke(readGcpDispatchHost(process.env, localOrigin));
}
