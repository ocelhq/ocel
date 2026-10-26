import { newInProcessImageOrigin } from "@framework/next-image-optimizer/in-process-origin";
import {
  newImageEndpointInvoke,
  servesImageEndpoint,
} from "@framework/next-runtime/image-endpoint";
import type { Invoke } from "@framework/node-runtime/host";
import { readAssetStorage } from "./asset-storage.mjs";
import { newCloudStorageObjectStore } from "./cloud-storage-assets.mjs";

export function newGcpImageEndpointInvoke(next: Invoke, env: NodeJS.ProcessEnv): Invoke {
  const storage = readAssetStorage(env);
  if (!storage || !servesImageEndpoint(env)) return next;
  return newImageEndpointInvoke(next, newInProcessImageOrigin(newCloudStorageObjectStore(storage)));
}
