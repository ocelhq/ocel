import type { NextHost } from "@framework/next-runtime/host";
import { newAwsCacheStore } from "./cache-store.mjs";
import { cloudFrontTagsPerObject } from "./cloudfront-tags.mjs";
import { newAwsTagPublisher } from "./tag-snapshot-store.mjs";
import { newAwsUseCacheStore } from "./use-cache-store.mjs";

export function newAwsContainerNextHost(): NextHost {
  return {
    cacheTagsPerObject: cloudFrontTagsPerObject,
    newCacheStore: async () => newAwsCacheStore(),
    newUseCacheStore: async () => newAwsUseCacheStore(newAwsTagPublisher()),
  };
}
