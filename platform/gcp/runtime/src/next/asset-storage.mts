import { bucketVars } from "@platform/edge-contract/storage-key";
import { newCloudStorage } from "./cloud-storage.mjs";

const storageEndpointVar = "OCEL_STORAGE_ENDPOINT";

export function readAssetStorage(env: NodeJS.ProcessEnv) {
  const bucket = env[bucketVars.edge];
  if (!bucket) return undefined;
  return newCloudStorage({ bucket, endpoint: env[storageEndpointVar] });
}
