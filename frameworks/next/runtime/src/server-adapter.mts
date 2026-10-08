import { refuseAppCacheHandlers } from "@framework/next-cache/app-cache-handlers";
import { installCacheHandlers } from "./cache-handlers.mjs";

const productionServerPhase = "phase-production-server";

export interface NextServerAdapter {
  name: string;
  modifyConfig(config: Record<string, any>, ctx: { phase: string }): Record<string, any>;
}

export function newServerAdapter(installHost: () => void): NextServerAdapter {
  let installed = false;
  return {
    name: "ocel-server",
    modifyConfig(config, { phase }) {
      if (phase !== productionServerPhase) return config;
      refuseAppCacheHandlers(config);
      if (!installed) {
        installed = true;
        installHost();
        installCacheHandlers();
      }
      return { ...config, cacheMaxMemorySize: 0 };
    },
  };
}
