import { createConnectTransport } from "@connectrpc/connect-node";
import type { Bucket } from "./bucket.js";
import { createBucketClient, type BucketServiceClient } from "./bucket-client.js";

/**
 * The typed client plus the resolved store bucket name to address. The single
 * seam between the environment-blind SDK and the injected runtime address;
 * nothing here branches on prod/dev.
 */
export interface BucketContext {
  client: BucketServiceClient;
  bucket: string;
}

export function resolveBucketContext(bucket: Bucket): BucketContext {
  const { address, bucket: storeBucket } = bucket.__config();
  const transport = createConnectTransport({
    httpVersion: "1.1",
    baseUrl: address,
  });
  return { client: createBucketClient(transport), bucket: storeBucket };
}
