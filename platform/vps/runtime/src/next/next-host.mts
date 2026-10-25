import { newInProcessImageOrigin } from "@framework/next-image-optimizer/in-process-origin";
import {
  type DispatchHost,
  newDispatchInvoke,
  readDispatchHost,
} from "@framework/next-runtime/dispatch-host";
import type { NextHost } from "@framework/next-runtime/host";
import { fetchUndecoded } from "@framework/node-runtime/undecoded-fetch";
import { readStorageCoordinate } from "@platform/edge-contract/runtime-env";
import { newDiskAssetBucket, newDiskObjectStore } from "./disk-assets.mjs";

const buildDirVar = "OCEL_BUILD_DIR";

function requiredDir(env: NodeJS.ProcessEnv, name: string): string {
  const dir = env[name];
  if (!dir) throw new Error(`ocel: ${name} is unset: run this image through ocel deploy`);
  return dir;
}

export function readVpsDispatchHost(env: NodeJS.ProcessEnv, localOrigin: string): DispatchHost {
  const buildDir = requiredDir(env, buildDirVar);
  const storage = readStorageCoordinate(env);
  return readDispatchHost(env, localOrigin, {
    ...(storage && {
      assetBucket: newDiskAssetBucket(buildDir, storage),
      imageOrigin: newInProcessImageOrigin(newDiskObjectStore(buildDir, storage)),
    }),
    originFetch: fetchUndecoded,
  });
}

export function newVpsNextHost(env: NodeJS.ProcessEnv): NextHost {
  requiredDir(env, buildDirVar);
  return {
    newDispatchInvoke: async (localOrigin) =>
      newDispatchInvoke(readVpsDispatchHost(env, localOrigin)),
  };
}
