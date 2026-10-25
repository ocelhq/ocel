import type { CacheStore } from "@framework/next-runtime/cache-store";
import { bucketVars } from "@platform/edge-contract/runtime-env";
import { newS3CacheStore } from "@platform/s3-runtime/cache-store";
import { newS3Client } from "../cache/sdk-clients.mjs";
import { requireEnv, requireStorageCoordinate } from "./required-env.mjs";

export function newAwsCacheStore(): CacheStore {
  const coordinate = requireStorageCoordinate();
  const client = newS3Client();
  return newS3CacheStore({
    pages: { bucket: requireEnv(bucketVars.edge), client },
    fetches: { bucket: requireEnv(bucketVars.cache), client },
    coordinate,
  });
}
