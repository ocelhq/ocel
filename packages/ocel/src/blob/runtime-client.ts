import { type Client, createClient, type Transport } from "@connectrpc/connect";
import { RuntimeService } from "../gen/proto/runtime/v1/runtime_pb";

/**
 * The generated, fully-typed client for runtime.v1.RuntimeService. This is the
 * single seam SDK code uses to reach runtime mechanics (presign, verify,
 * status); nothing else imports the generated service directly.
 */
export type RuntimeServiceClient = Client<typeof RuntimeService>;

/**
 * Wraps a caller-provided transport in the typed RuntimeService client. The
 * transport is injected (constructed by the dev bridge from the resolved
 * address); this seam never constructs one itself.
 */
export function createRuntimeClient(transport: Transport): RuntimeServiceClient {
  return createClient(RuntimeService, transport);
}
