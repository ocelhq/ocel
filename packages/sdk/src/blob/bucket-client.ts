import { type Client, createClient, type Transport } from "@connectrpc/connect";
import { BucketService } from "../gen/proto/buckets/v1/buckets_pb.js";

/**
 * The generated, fully-typed client for buckets.v1.BucketService. This is the
 * single seam SDK code uses to reach runtime mechanics (presign, verify,
 * status); nothing else imports the generated service directly.
 */
export type BucketServiceClient = Client<typeof BucketService>;

/**
 * Wraps a caller-provided transport in the typed BucketService client. The
 * transport is injected (constructed by the dev bridge from the resolved
 * address); this seam never constructs one itself.
 */
export function createBucketClient(transport: Transport): BucketServiceClient {
  return createClient(BucketService, transport);
}
