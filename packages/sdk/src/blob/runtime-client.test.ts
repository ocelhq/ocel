import type { Client } from "@connectrpc/connect";
import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, expectTypeOf, it } from "vitest";
import { RuntimeService } from "../gen/proto/runtime/v1/runtime_pb";
import {
  createRuntimeClient,
  type RuntimeServiceClient,
} from "./runtime-client";

describe("createRuntimeClient", () => {
  it("is typed as the generated RuntimeService client interface", () => {
    expectTypeOf<RuntimeServiceClient>().toEqualTypeOf<
      Client<typeof RuntimeService>
    >();
    expectTypeOf(createRuntimeClient).returns.toEqualTypeOf<
      Client<typeof RuntimeService>
    >();
  });

  it("wraps an injected transport, exposing the three RPCs", () => {
    const transport = createRouterTransport(() => {});
    const client = createRuntimeClient(transport);

    expect(typeof client.presignUpload).toBe("function");
    expect(typeof client.verifyUploadSignature).toBe("function");
    expect(typeof client.getUploadStatus).toBe("function");
  });
});
