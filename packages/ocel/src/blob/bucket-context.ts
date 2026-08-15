import { createConnectTransport } from "@connectrpc/connect-node";
import type { Interceptor } from "@connectrpc/connect";
import type { Bucket } from "./bucket.js";
import { getRuntimeAddress, getSessionToken } from "../utils/get-config.js";
import { createBucketClient, type BucketServiceClient } from "./bucket-client.js";

export interface BucketContext {
  client: BucketServiceClient;
  bucket: string;
}

function authorized(token: string): Interceptor {
  return (next) => (req) => {
    req.header.set("Authorization", `Bearer ${token}`);
    return next(req);
  };
}

export function resolveBucketContext(bucket: Bucket): BucketContext {
  const { bucket: storeBucket } = bucket.__config();
  const token = getSessionToken();
  const transport = createConnectTransport({
    httpVersion: "1.1",
    baseUrl: getRuntimeAddress(),
    interceptors: token ? [authorized(token)] : [],
  });
  return { client: createBucketClient(transport), bucket: storeBucket };
}
