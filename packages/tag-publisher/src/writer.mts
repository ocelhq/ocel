import { createHmac } from "node:crypto";

import type { TagRecord } from "@ocel/next-cache";

// The publisher's half of the ISR writer contract (workers/isr-writer): raising
// a build's invalidations into the snapshot Durable Object that owns its edge
// replica.

// isrWriteSecret is the Go deploy host's derivation, spelled again here because
// this is the second holder of the substrate's seed: HMAC-SHA256 over the
// build's own isrPrefix, hex. Deriving is what keeps this function off a second
// credential path — the secret it produces is the same one that build's Lambdas
// were injected with, and the only one its Durable Object will accept.
export function isrWriteSecret(seed: string, isrPrefix: string): string {
  return createHmac("sha256", seed).update(isrPrefix).digest("hex");
}

// raise posts one build's records to its snapshot object and returns once R2
// holds them. Anything but a 204 throws, which fails the batch: a raise that
// did not land is an invalidation the edge will never serve, so it is retried
// and, if it keeps failing, retired to the dead-letter queue where the alarm
// can see it. That includes the 429 the writer answers when its own publisher
// exhausted its retries — nothing durable happened there, and the records are
// still in hand, so reposting them is the whole of the repair.
export async function raise(
  fetchImpl: typeof fetch,
  endpoint: string,
  seed: string,
  isrPrefix: string,
  records: Map<string, TagRecord>,
): Promise<void> {
  const response = await fetchImpl(`${endpoint}/${isrPrefix}/tags`, {
    method: "POST",
    headers: {
      authorization: `Bearer ${isrWriteSecret(seed, isrPrefix)}`,
      "content-type": "application/json",
    },
    body: JSON.stringify({ records: Object.fromEntries(records) }),
  });
  if (response.status !== 204) {
    throw new Error(`raise ${isrPrefix}: writer answered ${response.status}`);
  }
}
