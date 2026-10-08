import {
  type CacheHandlerSettings,
  refuseAppCacheHandlers,
} from "@framework/next-cache/app-cache-handlers";
import { installCacheHandlers } from "./cache-handlers.mjs";
import { getNextHost, installNextHostOnFirstUse, type NextHost } from "./host.mjs";

const nextRouterServers = Symbol.for("@next/router-server-methods");

type NextRouterServer = Record<PropertyKey, unknown> & { nextConfig?: CacheHandlerSettings };

function refuseAppCacheHandlersThenBuildHost(config: CacheHandlerSettings): void {
  refuseAppCacheHandlers(config);
  getNextHost();
}

function newNextRouterServer(server: NextRouterServer): NextRouterServer {
  return new Proxy(server, {
    set(target, key, value) {
      if (key === "nextConfig") refuseAppCacheHandlersThenBuildHost(value);
      target[key] = value;
      return true;
    },
  });
}

function newNextRouterServers(): Record<string, NextRouterServer> {
  return new Proxy({} as Record<string, NextRouterServer>, {
    set(servers, dir: string, server: NextRouterServer) {
      if (server.nextConfig) refuseAppCacheHandlersThenBuildHost(server.nextConfig);
      servers[dir] = newNextRouterServer(server);
      return true;
    },
  });
}

export function installServerPreload(newHost: () => NextHost): void {
  installNextHostOnFirstUse(newHost);
  installCacheHandlers();
  (globalThis as Record<symbol, unknown>)[nextRouterServers] = newNextRouterServers();
}
