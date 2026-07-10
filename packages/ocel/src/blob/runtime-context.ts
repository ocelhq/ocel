import { createConnectTransport } from "@connectrpc/connect-node";
import type { Bucket } from "./bucket";
import { createRuntimeClient, type RuntimeServiceClient } from "./runtime-client";

/**
 * The typed client plus the resolved store bucket name to address. The single
 * seam between the environment-blind SDK and the injected runtime address;
 * nothing here branches on prod/dev.
 */
export interface RuntimeContext {
  client: RuntimeServiceClient;
  bucket: string;
}

export function resolveRuntimeContext(bucket: Bucket): RuntimeContext {
  const { address, bucket: storeBucket } = bucket.__config();
  const transport = createConnectTransport({
    httpVersion: "1.1",
    baseUrl: address,
  });
  return { client: createRuntimeClient(transport), bucket: storeBucket };
}
