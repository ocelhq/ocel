import { addCacheHandlers } from "@framework/next-cache/naming";

const productionServerPhase = "phase-production-server";

export interface NextServerAdapter {
  name: string;
  modifyConfig(config: Record<string, any>, ctx: { phase: string }): Record<string, any>;
}

export function newServerAdapter(runtimeDir: string, installHost: () => void): NextServerAdapter {
  let installed = false;
  return {
    name: "ocel-server",
    modifyConfig(config, { phase }) {
      if (phase !== productionServerPhase) return config;
      if (!installed) {
        installed = true;
        installHost();
      }
      return {
        ...config,
        cacheMaxMemorySize: 0,
        ...addCacheHandlers(config, runtimeDir),
      };
    },
  };
}
