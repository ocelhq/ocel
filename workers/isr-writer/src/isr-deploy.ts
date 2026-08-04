import { DurableObject } from "cloudflare:workers";

import * as registry from "./registry";
import type { Env } from "./env";

// One instance per deploy, addressed by that deploy's isrPrefix
// (<env>/<project>/<app>/<buildId>, already exactly per-deploy). It carries no
// auth logic of its own: index.ts decides who may call which method before a
// call ever reaches here, so the DO stays unauthenticated behind the worker
// boundary — the same split workers/deployments-store uses.
export class IsrDeploy extends DurableObject<Env> {
  async initialize(secretHash: string): Promise<void> {
    registry.initialize(this.ctx.storage, secretHash);
  }

  async secretHash(): Promise<string | undefined> {
    return registry.secretHash(this.ctx.storage);
  }

  // Retires the deploy: its secret hash is gone, so every entry write signed
  // with it is refused from here on, and the same isrPrefix is back to being
  // one a later initialize can claim.
  async destroy(): Promise<void> {
    await this.ctx.storage.deleteAll();
  }
}
