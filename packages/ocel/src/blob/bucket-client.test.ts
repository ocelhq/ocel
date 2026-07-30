import type { Client } from "@connectrpc/connect";
import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, expectTypeOf, it } from "vitest";
import { BucketService } from "../gen/proto/buckets/v1/buckets_pb.js";
import {
  createBucketClient,
  type BucketServiceClient,
} from "./bucket-client.js";

describe("createBucketClient", () => {
  it("is typed as the generated BucketService client interface", () => {
    expectTypeOf<BucketServiceClient>().toEqualTypeOf<
      Client<typeof BucketService>
    >();
    expectTypeOf(createBucketClient).returns.toEqualTypeOf<
      Client<typeof BucketService>
    >();
  });

  it("wraps an injected transport, exposing the three RPCs", () => {
    const transport = createRouterTransport(() => {});
    const client = createBucketClient(transport);

    expect(typeof client.presignUpload).toBe("function");
    expect(typeof client.verifyUploadSignature).toBe("function");
    expect(typeof client.getUploadStatus).toBe("function");
  });
});
