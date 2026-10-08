import {
  type CacheHandlerSettings,
  refuseAppCacheHandlers,
} from "@framework/next-cache/app-cache-handlers";
import { installCacheHandlers } from "./cache-handlers.mjs";
import { getNextHost, installNextHostOnFirstUse, type NextHost } from "./host.mjs";

const nextRouterServerContexts = Symbol.for("@next/router-server-methods");

type NextServerContext = Record<PropertyKey, unknown> & { nextConfig?: CacheHandlerSettings };

function startNextServer(config: CacheHandlerSettings): void {
  refuseAppCacheHandlers(config);
  getNextHost();
}

function newNextServerContext(context: NextServerContext): NextServerContext {
  if (context.nextConfig) startNextServer(context.nextConfig);
  return new Proxy(context, {
    set(target, key, value) {
      if (key === "nextConfig") startNextServer(value);
      target[key] = value;
      return true;
    },
  });
}

function newNextServerContexts(): Record<string, NextServerContext> {
  return new Proxy({} as Record<string, NextServerContext>, {
    set(contexts, dir: string, context: NextServerContext) {
      contexts[dir] = newNextServerContext(context);
      return true;
    },
  });
}

export function installServerPreload(newHost: () => NextHost): void {
  installNextHostOnFirstUse(newHost);
  installCacheHandlers();
  (globalThis as Record<symbol, unknown>)[nextRouterServerContexts] = newNextServerContexts();
}
