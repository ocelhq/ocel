import OcelCacheHandler from "./cache-handler.mjs";
import useCacheDefault from "./use-cache-default.mjs";
import useCacheRemote from "./use-cache-remote.mjs";

const nextCacheHandlersKey = Symbol.for("@next/cache-handlers");

export function installCacheHandlers(): typeof OcelCacheHandler {
  (globalThis as Record<symbol, unknown>)[nextCacheHandlersKey] = {
    FetchCache: OcelCacheHandler,
    DefaultCache: useCacheDefault,
    RemoteCache: useCacheRemote,
  };
  return OcelCacheHandler;
}
