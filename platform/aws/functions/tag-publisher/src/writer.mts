import { createHmac } from "node:crypto";

import type { TagRecord } from "@framework/next-cache";
import { newIsrWriterClient } from "@platform/edge-contract/isr-writer";

export function isrWriteSecret(seed: string, isrPrefix: string): string {
  return createHmac("sha256", seed).update(isrPrefix).digest("hex");
}

export async function raise(
  fetchImpl: typeof fetch,
  endpoint: string,
  seed: string,
  isrPrefix: string,
  records: Map<string, TagRecord>,
): Promise<void> {
  await newIsrWriterClient({
    endpoint,
    isrPrefix,
    secret: isrWriteSecret(seed, isrPrefix),
    fetch: fetchImpl,
  }).raiseTags(Object.fromEntries(records));
}
