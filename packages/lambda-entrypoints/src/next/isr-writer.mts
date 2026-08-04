import type { CacheEntryFile } from "@ocel/next-cache";

// The origin's half of the ISR writer contract (workers/isr-writer). Route
// entries are written through the account-level writer worker, which holds the
// bucket as a native binding — so this function needs no object-store
// credentials of its own to write one, only a per-deploy secret that rotates
// with the build.

// Both coordinates are plain env vars the deploy injects (cloud/aws/deploy,
// isrConfig.env). They are deliberately not an SSM SecureString: a GetParameter
// on the cold path would buy protection a per-deploy secret that rotates every
// build does not need.
const writerURLEnv = "OCEL_ISR_WRITER_URL";
const writerSecretEnv = "OCEL_ISR_WRITER_SECRET";

export interface EntryWriter {
  write(key: string, entry: CacheEntryFile): Promise<void>;
}

// isrEntryWriter is the writer this deploy was pointed at, or null when the
// substrate adopted none — in which case entries keep going straight to the
// object store. Both coordinates are written together or neither is.
export function isrEntryWriter(fetchImpl: typeof fetch = fetch): EntryWriter | null {
  const url = process.env[writerURLEnv];
  const secret = process.env[writerSecretEnv];
  if (!url || !secret) return null;
  return writerAt(url, secret, fetchImpl);
}

export function writerAt(url: string, secret: string, fetchImpl: typeof fetch = fetch): EntryWriter {
  return {
    async write(key, entry) {
      const res = await fetchImpl(`${url}?key=${encodeURIComponent(key)}`, {
        method: "PUT",
        headers: {
          authorization: `Bearer ${secret}`,
          "content-type": "application/json",
        },
        body: JSON.stringify(entry),
      });
      if (res.ok) return;
      // R2 caps concurrent writes to one key at 1/sec and the writer reports the
      // loser as 429. Both racers rendered the same route fresh, so the write
      // that lost is redundant rather than dropped work — retrying it would turn
      // a herd into a retry storm for no gain.
      if (res.status === 429) return;
      throw new Error(
        `ocel cache handler: isr writer rejected ${key}: status ${res.status}`,
      );
    },
  };
}
