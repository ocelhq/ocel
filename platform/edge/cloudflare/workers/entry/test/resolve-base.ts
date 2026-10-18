import { env } from "cloudflare:test";
import type { ResolveBase } from "../src/index";
import type { ServeFactory } from "../src/serve-objects";
import type { ServingPorts } from "../src/serving";
import { fakeServing } from "./serving-deps";

declare module "cloudflare:test" {
  interface ProvidedEnv {
    LOADER: WorkerLoader;
  }
}

export function servingPorts(serve: ServeFactory = fakeServing().serving.serve): ServingPorts {
  return { serve, queue: { raise: async () => {} }, waitUntil: () => {} };
}

export function resolveBase(over: Partial<ResolveBase> = {}): ResolveBase {
  return {
    assetStore: {
      cache: { match: async () => undefined, put: async () => {} },
      waitUntil: () => {},
    },
    serving: servingPorts(),
    edgeRuntime: { loader: env.LOADER },
    ...over,
  };
}
