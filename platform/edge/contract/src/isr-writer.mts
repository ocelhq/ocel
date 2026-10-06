import {
  type CacheEntryFile,
  entryMissHeader,
  readableSnapshot,
  type TagRecord,
  type TagSnapshot,
} from "@framework/next-cache";

const writeTimeoutMs = 10_000;

const readTimeoutMs = 3_000;

export type TagSnapshotRead =
  | { kind: "fresh"; snapshot: TagSnapshot; etag: string | null }
  | { kind: "unchanged" }
  | { kind: "absent" };

export interface IsrWriterClient {
  readEntry(key: string): Promise<CacheEntryFile | null>;
  writeEntry(key: string, entry: CacheEntryFile): Promise<void>;
  raiseTags(records: Record<string, TagRecord>): Promise<void>;
  readTagSnapshot(etag: string | null): Promise<TagSnapshotRead>;
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

export function newIsrWriterClient(opts: {
  endpoint: string;
  isrPrefix: string;
  secret: string;
  fetch?: typeof fetch;
}): IsrWriterClient {
  const { endpoint, isrPrefix, secret } = opts;
  const send = opts.fetch ?? fetch;
  if (!endpoint.startsWith("https://")) {
    throw new Error(
      `isr writer ${isrPrefix}: the endpoint ${endpoint} is not https, and the write secret travels in the clear over anything else`,
    );
  }
  const base = `${endpoint}/${isrPrefix}`;
  const entryAt = (key: string) => `${base}/entry?key=${encodeURIComponent(key)}`;
  const bearer = `Bearer ${secret}`;

  return {
    async readEntry(key) {
      try {
        const res = await send(entryAt(key), {
          headers: { authorization: bearer },
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

    async writeEntry(key, entry) {
      const res = await send(entryAt(key), {
        method: "PUT",
        headers: { authorization: bearer, "content-type": "application/json" },
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
      throw new Error(`ocel cache handler: isr writer rejected ${key}: status ${res.status}`);
    },

    async raiseTags(records) {
      const res = await send(`${base}/tags`, {
        method: "POST",
        headers: { authorization: bearer, "content-type": "application/json" },
        body: JSON.stringify({ records }),
        signal: AbortSignal.timeout(writeTimeoutMs),
      });
      if (res.status !== 204) {
        throw new Error(`raise ${isrPrefix}: writer answered ${res.status}`);
      }
    },

    async readTagSnapshot(etag) {
      const headers: Record<string, string> = { authorization: bearer };
      if (etag !== null) headers["if-none-match"] = etag;
      const res = await send(`${base}/tags`, {
        headers,
        signal: AbortSignal.timeout(readTimeoutMs),
      });
      if (res.status === 304) return { kind: "unchanged" };
      if (res.status === 404 && res.headers.get(entryMissHeader) !== null) {
        return { kind: "absent" };
      }
      if (res.status !== 200) {
        throw new Error(`isr writer tag read ${isrPrefix}: status ${res.status}`);
      }
      const snapshot = readableSnapshot(await res.json());
      if (snapshot === null) {
        throw new Error(`isr writer tag read ${isrPrefix}: unreadable snapshot version`);
      }
      return { kind: "fresh", snapshot, etag: res.headers.get("etag") };
    },
  };
}
