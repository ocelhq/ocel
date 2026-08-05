import { handle, type BatchResponse, type SqsRecord } from "./handle.mjs";

// The account's revalidation consumer.
//
// One FIFO message is one route whose entry went stale and whose colo was
// admitted to refresh it. The queue has already deduplicated every other colo
// asking for the same render; this function's whole job is to turn the surviving
// message into one signed HEAD at the origin, which renders and writes through
// its own cache handler. The store is the origin's to write — the consumer
// never touches it, which is also what keeps it blind to any framework's entry
// format.
//
// Everything it needs is in its own environment (see README): the asset bucket
// holding the deploy records it resolves origins from, and the role credentials
// Lambda hands it. Both are read per invocation, because a container outlives
// the credentials it started with.
//
// The origin memo is built here, per invocation, so a redeploy is never one
// container's stale idea of where an app lives.
export const handler = async (event: { Records?: SqsRecord[] }): Promise<BatchResponse> =>
  handle(
    {
      fetch,
      credentials: {
        accessKeyId: process.env.AWS_ACCESS_KEY_ID ?? "",
        secretAccessKey: process.env.AWS_SECRET_ACCESS_KEY ?? "",
        sessionToken: process.env.AWS_SESSION_TOKEN,
      },
      bucket: process.env.OCEL_ASSET_BUCKET,
      region: process.env.AWS_REGION,
      origins: new Map(),
    },
    event,
  );
