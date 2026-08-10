import { type Client, createClient, type Transport } from "@connectrpc/connect";
import { BucketService } from "../gen/proto/buckets/v1/buckets_pb.js";

export type BucketServiceClient = Client<typeof BucketService>;

export function createBucketClient(transport: Transport): BucketServiceClient {
  return createClient(BucketService, transport);
}
