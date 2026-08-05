import { handle, type BatchResponse, type SqsRecord } from "./handle.mjs";
import { permittedHosts } from "./message.mjs";

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
// Everything it needs is in its own environment (see README): the Function URL
// hosts it may trigger, and the role credentials Lambda hands it. Both are read
// per invocation, because a container outlives the credentials it started with.
export const handler = async (event: { Records?: SqsRecord[] }): Promise<BatchResponse> =>
  handle(
    {
      fetch,
      credentials: {
        accessKeyId: process.env.AWS_ACCESS_KEY_ID ?? "",
        secretAccessKey: process.env.AWS_SECRET_ACCESS_KEY ?? "",
        sessionToken: process.env.AWS_SESSION_TOKEN,
      },
      hosts: permittedHosts(process.env.OCEL_REVALIDATE_ALLOWED_HOSTS),
    },
    event,
  );
