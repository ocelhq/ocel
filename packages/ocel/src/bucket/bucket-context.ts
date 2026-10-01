import { createRuntimeTransport } from "../runtime/transport.js";
import type { Bucket } from "./bucket.js";
import { type BucketServiceClient, createBucketClient } from "./bucket-client.js";

export interface BucketContext {
  client: BucketServiceClient;
  bucket: string;
  publicBaseUrl: string;
}

export function resolveBucketContext(bucket: Bucket): BucketContext {
  const { bucket: storeBucket, publicBaseUrl } = bucket.__config();
  return {
    client: createBucketClient(createRuntimeTransport()),
    bucket: storeBucket,
    publicBaseUrl,
  };
}
