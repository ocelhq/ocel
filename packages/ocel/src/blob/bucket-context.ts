import { createConnectTransport } from "@connectrpc/connect-node";
import type { Bucket } from "./bucket.js";
import { getRuntimeAddress } from "../utils/get-config.js";
import { createBucketClient, type BucketServiceClient } from "./bucket-client.js";

export interface BucketContext {
  client: BucketServiceClient;
  bucket: string;
}

export function resolveBucketContext(bucket: Bucket): BucketContext {
  const { bucket: storeBucket } = bucket.__config();
  const transport = createConnectTransport({
    httpVersion: "1.1",
    baseUrl: getRuntimeAddress(),
  });
  return { client: createBucketClient(transport), bucket: storeBucket };
}
