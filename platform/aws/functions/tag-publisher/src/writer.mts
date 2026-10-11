import type { TagRecord } from "@framework/next-cache";
import { newIsrWriterClient } from "@platform/edge-contract/isr-writer";
import { derivePurgeSecret } from "@platform/edge-contract/purge";

export async function raise(
  send: typeof fetch,
  endpoint: string,
  seed: string,
  isrPrefix: string,
  records: Map<string, TagRecord>,
): Promise<void> {
  await newIsrWriterClient({
    endpoint,
    isrPrefix,
    secret: derivePurgeSecret(seed, isrPrefix),
    fetch: send,
  }).raiseTags(Object.fromEntries(records));
}
