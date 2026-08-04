import type { CacheEntryFile } from "@ocel/next-cache";

// The origin's half of the ISR writer contract (workers/isr-writer). Route
// entries are both read and written through the account-level writer worker,
// which holds the bucket as a native binding — so this function needs no
// object-store credentials of its own for entries at all, only a per-deploy
// secret that rotates with the build. Reads go through it for the same reason
// writes do: an R2 token scopes to a bucket and nothing finer, so one left here
// to read with would still be one that can write every project's entries.

// Both coordinates are plain env vars the deploy injects (cloud/aws/deploy,
// isrConfig.env). They are deliberately not an SSM SecureString: a GetParameter
// on the cold path would buy protection a per-deploy secret that rotates every
// build does not need.
const writerURLEnv = "OCEL_ISR_WRITER_URL";
const writerSecretEnv = "OCEL_ISR_WRITER_SECRET";

// The PutObjectCommand this replaced carried the SDK's own connect and request
// timeouts; a bare fetch carries none, and a hung writer would hold the
// invocation open — and billing — to the function's timeout. Long enough for a
// whole prerender payload over a cold connection, short enough to be a fraction
// of any function's budget.
const writeTimeoutMs = 10_000;

// A read sits on the serving path, and what it buys by waiting is one saved
// render. Waiting longer than a render takes is therefore never right, however
// close the entry is to arriving — so this is deliberately a small fraction of
// the write's budget rather than the same number.
const readTimeoutMs = 3_000;

export interface EntryStore {
  // Never rejects. See read() below: an entry read is on the serving path.
  read(key: string): Promise<CacheEntryFile | null>;
  write(key: string, entry: CacheEntryFile): Promise<void>;
}

// A rejection the writer will give again for the same key however many times it
// is asked: a malformed key, a secret this build no longer holds. Distinct from
// a 429 (which is benign) and from a 5xx or a timeout (which the next
// revalidation retries by itself), because a permanent rejection means this
// route renders on every request and never caches — so it is logged where it
// happens rather than left to whatever drains the deferred write.
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

// isrEntryStore is the writer this deploy was pointed at. Absent coordinates
// are a broken deploy rather than a mode: entries a deploy with an adopted cache
// store writes anywhere else are written where nothing reads them.
export function isrEntryStore(fetchImpl: typeof fetch = fetch): EntryStore {
  const url = process.env[writerURLEnv];
  const secret = process.env[writerSecretEnv];
  if (!url || !secret) {
    throw new Error(
      `ocel cache handler: ${writerURLEnv} and ${writerSecretEnv} must both be set ` +
        `when this deploy reads its ISR entries from an adopted cache store; ` +
        "re-run `ocel bootstrap` and redeploy",
    );
  }
  return entryStoreAt(url, secret, fetchImpl);
}

export function entryStoreAt(
  url: string,
  secret: string,
  fetchImpl: typeof fetch = fetch,
): EntryStore {
  // One spelling of the key for both ops, so the worker derives one object key
  // from whichever of them asked.
  const at = (key: string) => `${url}?key=${encodeURIComponent(key)}`;

  return {
    // FAILS OPEN, unconditionally. Next calls get() on the serving path for
    // every request to a cached route and does not wrap it, so an error raised
    // here is a broken request rather than a slow one. Every failure — an
    // unreachable writer, a timeout, a 5xx, a rejected credential, a body that
    // will not parse — is therefore a miss, which makes Next render: a writer
    // outage costs latency and origin load, and serves every page correctly
    // throughout.
    async read(key) {
      try {
        const res = await fetchImpl(at(key), {
          headers: { authorization: `Bearer ${secret}` },
          signal: AbortSignal.timeout(readTimeoutMs),
        });
        // The ordinary miss, and the only one that is not worth a word.
        if (res.status === 404) return null;
        if (!res.ok) throw new Error(`status ${res.status}`);
        return (await res.json()) as CacheEntryFile;
      } catch (err) {
        // Invisible in the response — the page is served, just rendered — so
        // the log is the only place a writer that has stopped answering shows
        // up before the origin bill does.
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
      // R2 caps concurrent writes to one key at 1/sec and the writer reports the
      // loser as 429. Both racers rendered the same route fresh, so the write
      // that lost is redundant rather than dropped work — retrying it would turn
      // a herd into a retry storm for no gain.
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
