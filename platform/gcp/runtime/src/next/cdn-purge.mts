import { type PublishTag, storedCacheTags } from "@framework/next-cache";
import { cloudCdnTagLimits } from "./cloud-cdn.mjs";
import { newMetadataToken } from "./metadata-token.mjs";

export interface CdnPurgeOptions {
  urlMap: string;
  release: string;
  fetch?: typeof fetch;
  metadataOrigin?: string;
  now?: () => number;
  sleep?: (ms: number) => Promise<void>;
  random?: () => number;
  defer?: (run: () => void) => void;
  requestTimeoutMs?: number;
  origin?: string;
  warn?: (message: string) => void;
}

export type PurgeTags = (tags: readonly string[]) => Promise<void>;

const attempts = 4;
const tagsPerRequest = 10;

export function withCdnPurge(publish: PublishTag, purge: PurgeTags): PublishTag {
  return (tag, record) => publish(tag, record).then(() => purge([tag]));
}

export function newCdnPurge(options: CdnPurgeOptions): PurgeTags {
  const { urlMap, release } = options;
  const origin = (options.origin ?? "https://compute.googleapis.com").replace(/\/$/, "");
  const url = `${origin}/compute/v1/${urlMap}/invalidateCache`;
  const doFetch = options.fetch ?? globalThis.fetch;
  const sleep =
    options.sleep ?? ((ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms)));
  const random = options.random ?? Math.random;
  const defer = options.defer ?? ((run: () => void) => void setImmediate(run));
  const requestTimeoutMs = options.requestTimeoutMs ?? 10_000;
  const warn = options.warn ?? console.warn;
  const metadataToken = newMetadataToken({
    service: "Cloud CDN",
    fetch: doFetch,
    metadataOrigin: options.metadataOrigin,
    now: options.now,
  });

  async function send(cacheTags: string[]): Promise<Response> {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), requestTimeoutMs);
    try {
      return await doFetch(url, {
        method: "POST",
        headers: {
          Authorization: `Bearer ${await metadataToken.token()}`,
          "Content-Type": "application/json",
        },
        body: JSON.stringify({ cacheTags }),
        signal: controller.signal,
      });
    } finally {
      clearTimeout(timer);
    }
  }

  async function purgeChunk(cacheTags: string[]): Promise<void> {
    let refreshed = false;
    let last = "";
    for (let attempt = 1; attempt <= attempts; attempt++) {
      if (attempt > 1) await sleep(random() * Math.min(2000, 100 * 2 ** (attempt - 2)));
      let res: Response;
      try {
        res = await send(cacheTags);
        if (res.status === 401 && !refreshed) {
          refreshed = true;
          metadataToken.forget();
          res = await send(cacheTags);
        }
      } catch (error) {
        last = error instanceof Error ? error.message : String(error);
        continue;
      }
      if (res.status === 200) return;
      last = String(res.status);
      if (res.status === 403) {
        throw new Error(
          `ocel: Cloud CDN refused to clear ${cacheTags.length} tag(s) of release ${release} on ${urlMap}: 403. The app's account lacks the cache purge role its deploy grants`,
        );
      }
      const retryable = res.status === 408 || res.status === 429 || res.status >= 500;
      if (!retryable) break;
    }
    throw new Error(
      `ocel: Cloud CDN did not clear tags of release ${release} on ${urlMap}: ${last}`,
    );
  }

  let pending = new Map<string, (chunk: Promise<void>) => void>();
  let scheduled = false;

  function flush(): void {
    const batch = pending;
    pending = new Map();
    scheduled = false;
    const stamped = [...batch.keys()];
    for (let i = 0; i < stamped.length; i += tagsPerRequest) {
      const chunk = stamped.slice(i, i + tagsPerRequest);
      const done = purgeChunk(chunk);
      done.catch(() => {});
      for (const tag of chunk) batch.get(tag)?.(done);
    }
  }

  return (tags) => {
    const { tags: stamped, unstorable, overflowed } = storedCacheTags(release, tags);
    const sendable = stamped.filter(
      (tag) => Buffer.byteLength(tag, "utf8") <= cloudCdnTagLimits.bytesPerTag,
    );
    if (unstorable.length > 0 || overflowed.length > 0 || sendable.length < stamped.length) {
      warn(
        `ocel: revalidating ${tags.join(", ")} cannot reach Cloud CDN: a tag stamped with its release must fit ${cloudCdnTagLimits.bytesPerTag} bytes`,
      );
    }
    if (sendable.length === 0) return Promise.resolve();
    const chunks = sendable.map(
      (tag) =>
        new Promise<Promise<void>>((resolve) => {
          const earlier = pending.get(tag);
          pending.set(tag, (chunk) => {
            earlier?.(chunk);
            resolve(chunk);
          });
        }),
    );
    if (!scheduled) {
      scheduled = true;
      defer(flush);
    }
    return Promise.all(chunks)
      .then((done) => Promise.all(done))
      .then(() => undefined);
  };
}
