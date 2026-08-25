import { entryMissHeader } from "@framework/next-cache";
import type { CacheEntryFile } from "@framework/next-cache";

const writerURLEnv = "OCEL_ISR_WRITER_URL";
const writerSecretEnv = "OCEL_ISR_WRITER_SECRET";

const writeTimeoutMs = 10_000;

const readTimeoutMs = 3_000;

export interface EntryStore {
  read(key: string): Promise<CacheEntryFile | null>;
  write(key: string, entry: CacheEntryFile): Promise<void>;
}

export class IsrWriteRejected extends Error {
  constructor(
    readonly key: string,
    readonly status: number,
  ) {
    super(
      `ocel cache handler: isr writer permanently rejected ${key}: status ${status}. ` +
        `This route will re-render on every request until the deploy is fixed.`,
    );
    this.name = "IsrWriteRejected";
  }
}

export function isrEntryStore(): EntryStore {
  const url = process.env[writerURLEnv];
  const secret = process.env[writerSecretEnv];
  if (!url || !secret) {
    throw new Error(
      `ocel cache handler: ${writerURLEnv} and ${writerSecretEnv} must both be set ` +
        `when this deploy reads its ISR entries from an adopted cache store; ` +
        "re-run `ocel bootstrap production` and redeploy",
    );
  }
  return entryStoreAt(url, secret);
}

export function entryStoreAt(
  url: string,
  secret: string,
  fetchImpl: typeof fetch = fetch,
): EntryStore {
  const at = (key: string) => `${url}?key=${encodeURIComponent(key)}`;

  return {
    async read(key) {
      try {
        const res = await fetchImpl(at(key), {
          headers: { authorization: `Bearer ${secret}` },
          signal: AbortSignal.timeout(readTimeoutMs),
        });
        if (res.status === 404) {
          if (res.headers.get(entryMissHeader) === null) {
            throw new Error("404 from a writer that reported no entry lookup");
          }
          return null;
        }
        if (!res.ok) throw new Error(`status ${res.status}`);
        return (await res.json()) as CacheEntryFile;
      } catch (err) {
        console.warn(
          `ocel cache handler: isr writer read of ${key} failed, serving as a cache miss: ${err}`,
        );
        return null;
      }
    },

    async write(key, entry) {
      const res = await fetchImpl(at(key), {
        method: "PUT",
        headers: {
          authorization: `Bearer ${secret}`,
          "content-type": "application/json",
        },
        body: JSON.stringify(entry),
        signal: AbortSignal.timeout(writeTimeoutMs),
      });
      if (res.ok) return;
      if (res.status === 429) return;
      if (res.status < 500) {
        const rejected = new IsrWriteRejected(key, res.status);
        console.error(rejected.message);
        throw rejected;
      }
      throw new Error(
        `ocel cache handler: isr writer rejected ${key}: status ${res.status}`,
      );
    },
  };
}
