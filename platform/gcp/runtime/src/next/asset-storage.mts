import { newCloudStorage } from "./cloud-storage.mjs";

const edgeBucketVar = "OCEL_EDGE_BUCKET";

const storageEndpointVar = "OCEL_STORAGE_ENDPOINT";

export function readAssetStorage(env: NodeJS.ProcessEnv) {
  const bucket = env[edgeBucketVar];
  if (!bucket) return undefined;
  return newCloudStorage({ bucket, endpoint: env[storageEndpointVar] });
}
