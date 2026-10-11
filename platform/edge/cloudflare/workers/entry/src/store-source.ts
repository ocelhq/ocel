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

function openR2Source(store: R2Bucket): StoreSource {
  return {
    async get(key) {
      const object = await store.get(key);
      return object ? { text: () => object.text() } : null;
    },
  };
}

export function openEntrySource(env: Env): StoreSource | undefined {
  const access = findBucketAccess(env);
  const bucket = access && openBucketSource(access);
  const r2 = env.OCEL_CACHE_STORE && openR2Source(env.OCEL_CACHE_STORE);
  if (!bucket || !r2) return bucket ?? r2;
  return {
    async get(key) {
      return (await bucket.get(key)) ?? r2.get(key);
    },
  };
}
