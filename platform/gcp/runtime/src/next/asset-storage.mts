import { newCloudStorage } from "./cloud-storage.mjs";

const assetBucketVar = "OCEL_ASSET_BUCKET";

const storageEndpointVar = "OCEL_STORAGE_ENDPOINT";

export function readAssetStorage(env: NodeJS.ProcessEnv) {
  const bucket = env[assetBucketVar];
  if (!bucket) return undefined;
  return newCloudStorage({ bucket, endpoint: env[storageEndpointVar] });
}
