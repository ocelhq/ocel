import { installNextHost } from "@framework/next-runtime/host";

installNextHost({
  newCacheStore: async () => (await import("./cache-store.mjs")).awsCacheStore(),
  newUseCacheStore: async () => (await import("./use-cache-store.mjs")).awsUseCacheStore(),
  newDispatchInvoke: async (localOrigin) =>
    (await import("./dispatch-host.mjs")).newAwsDispatchInvoke(localOrigin),
});

await import("@framework/next-runtime/entrypoint");
