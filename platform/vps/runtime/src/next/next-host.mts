import { newInProcessImageOrigin } from "@framework/next-image-optimizer/in-process-origin";
import type { NextHost } from "@framework/next-runtime/host";
import {
  newImageEndpointInvoke,
  servesImageEndpoint,
} from "@framework/next-runtime/image-endpoint";
import type { Invoke } from "@framework/node-runtime/host";
import { readStorageCoordinate } from "@platform/edge-contract/runtime-env";
import { newDiskObjectStore } from "./disk-assets.mjs";
import { openEdgePurge } from "./edge-purge.mjs";

const buildDirVar = "OCEL_BUILD_DIR";

function requiredDir(env: NodeJS.ProcessEnv, name: string): string {
  const dir = env[name];
  if (!dir) throw new Error(`ocel: ${name} is unset: run this image through ocel deploy`);
  return dir;
}

function newVpsImageEndpointInvoke(next: Invoke, env: NodeJS.ProcessEnv): Invoke {
  const storage = readStorageCoordinate(env);
  if (!storage || !servesImageEndpoint(env)) return next;
  const buildDir = requiredDir(env, buildDirVar);
  return newImageEndpointInvoke(
    next,
    newInProcessImageOrigin(newDiskObjectStore(buildDir, storage)),
  );
}

export function newVpsNextHost(env: NodeJS.ProcessEnv): NextHost {
  requiredDir(env, buildDirVar);
  const edgePurge = openEdgePurge(env);
  return {
    ...(edgePurge && { edgePurge }),
    newOriginInvoke: async (next) => newVpsImageEndpointInvoke(next, env),
  };
}
