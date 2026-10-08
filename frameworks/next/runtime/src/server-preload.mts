import { readFileSync } from "node:fs";
import net from "node:net";
import { join } from "node:path";
import {
  type CacheHandlerSettings,
  refuseAppCacheHandlers,
} from "@framework/next-cache/app-cache-handlers";
import { installCacheHandlers } from "./cache-handlers.mjs";
import { installNextHostOnFirstUse, type NextHost } from "./host.mjs";

const standaloneConfigVar = "__NEXT_PRIVATE_STANDALONE_CONFIG";
const buildManifest = join(".next", "required-server-files.json");

export function readServedCacheHandlers(
  env: NodeJS.ProcessEnv,
  projectDir: string,
): CacheHandlerSettings {
  if (env[standaloneConfigVar]) return JSON.parse(env[standaloneConfigVar]);
  let manifest: { config: CacheHandlerSettings };
  try {
    manifest = JSON.parse(readFileSync(join(projectDir, buildManifest), "utf8"));
  } catch (cause) {
    throw new Error(
      `ocel: cannot read ${buildManifest} in ${projectDir}, so cannot tell whether the app names its own cache handler`,
      { cause },
    );
  }
  const { config } = manifest;
  return {
    cacheHandler: config.cacheHandler || env.NEXT_CACHE_HANDLER_PATH,
    cacheHandlers: {
      default: config.cacheHandlers?.default || env.NEXT_DEFAULT_CACHE_HANDLER_PATH,
      remote: config.cacheHandlers?.remote || env.NEXT_REMOTE_CACHE_HANDLER_PATH,
    },
  };
}

function refuseAppCacheHandlersOnFirstListen(): void {
  const listen = net.Server.prototype.listen;
  let checked = false;
  net.Server.prototype.listen = function (this: net.Server, ...args: unknown[]) {
    if (!checked) {
      checked = true;
      refuseAppCacheHandlers(readServedCacheHandlers(process.env, process.cwd()));
    }
    return (listen as (...a: unknown[]) => net.Server).apply(this, args);
  } as typeof net.Server.prototype.listen;
}

export function installServerPreload(newHost: () => NextHost): void {
  installNextHostOnFirstUse(newHost);
  installCacheHandlers();
  refuseAppCacheHandlersOnFirstListen();
}
