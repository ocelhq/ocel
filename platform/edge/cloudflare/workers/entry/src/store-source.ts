import { type BucketAccess, findBucketAccess, signBucket } from "./bucket-access";
import type { Env } from "./env";
import { encodeKeyPath } from "./serve-key";
import type { StoreSource } from "./store-read";

function openBucketSource(access: BucketAccess): StoreSource {
  const client = signBucket(access);
  return {
    async get(key) {
      const res = await client.fetch(`${access.origin}/${encodeKeyPath(access.prefix + key)}`);
      if (res.ok) return { text: () => res.text() };
      await res.body?.cancel();
      if (res.status === 404 || res.status === 403) return null;
      throw new Error(`ocel: the bucket ${access.bucket} answered ${res.status} for ${key}`);
    },
  };
}

export function openEntrySource(env: Env): StoreSource | undefined {
  const access = findBucketAccess(env);
  return access?.holdsEntries ? openBucketSource(access) : undefined;
}
