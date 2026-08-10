import { createHmac } from "node:crypto";

import type { TagRecord } from "@ocel/next-cache";

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
