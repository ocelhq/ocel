import { type Client, createClient, type Transport } from "@connectrpc/connect";
import { BucketService } from "../gen/proto/app/blob/v1/blob_pb.js";

export type BucketServiceClient = Client<typeof BucketService>;

export function createBucketClient(transport: Transport): BucketServiceClient {
  return createClient(BucketService, transport);
}
