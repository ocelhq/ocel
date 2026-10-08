export interface CacheHandlerSettings {
  cacheHandler?: string;
  cacheHandlers?: { default?: string; remote?: string };
}

export function refuseAppCacheHandlers(config: CacheHandlerSettings): void {
  let setting: string;
  let envVar: string;
  if (config.cacheHandler) {
    setting = "cacheHandler";
    envVar = "NEXT_CACHE_HANDLER_PATH";
  } else if (config.cacheHandlers?.default) {
    setting = "cacheHandlers.default";
    envVar = "NEXT_DEFAULT_CACHE_HANDLER_PATH";
  } else if (config.cacheHandlers?.remote) {
    setting = "cacheHandlers.remote";
    envVar = "NEXT_REMOTE_CACHE_HANDLER_PATH";
  } else {
    return;
  }
  throw new Error(
    `ocel: ${setting} has Next use the app's own cache handler instead of the one Ocel installs, so this app's cache would bypass Ocel's store, revalidation and purges. Remove ${setting} from next.config, or unset ${envVar}`,
  );
}
