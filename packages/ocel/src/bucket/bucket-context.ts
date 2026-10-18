import type { Client } from "@connectrpc/connect";
import { BucketService } from "../gen/proto/app/bucket/v1/bucket_pb.js";
import type { BucketProperties } from "../gen/proto/common/bindings/v1/bindings_pb.js";
import { createRuntimeClient } from "../runtime/transport.js";

export type BucketServiceClient = Client<typeof BucketService>;

export interface BucketContext {
  client: BucketServiceClient;
  bucket: string;
  publicBaseUrl: string;
}

export function openBucketContext(properties: BucketProperties): BucketContext {
  return {
    client: createRuntimeClient(BucketService),
    bucket: properties.bucket,
    publicBaseUrl: properties.publicBaseUrl,
  };
}
