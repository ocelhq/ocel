import { DurableObject } from "cloudflare:workers";

import * as store from "./store";
import type {
  DeploymentRecord,
  HistoryEntry,
  Promotion,
  PruneResult,
} from "./store";
import type { Env } from "./env";

// One instance per project, addressed by a stable name (see index.ts). Every
// method is RPC-callable on the stub; the class itself carries no auth logic
// — index.ts decides who may call which method before it ever reaches here.
export class DeploymentsStore extends DurableObject<Env> {
  async putStaged(record: DeploymentRecord): Promise<void> {
    await store.putStaged(this.ctx.storage, record);
  }

  async promote(promotion: Promotion): Promise<void> {
    await store.promote(this.ctx.storage, promotion);
  }

  async activeBuildId(app: string): Promise<string | undefined> {
    return store.activeBuildId(this.ctx.storage, app);
  }

  async record(
    app: string,
    buildId: string,
  ): Promise<DeploymentRecord | undefined> {
    return store.record(this.ctx.storage, app, buildId);
  }

  async history(): Promise<HistoryEntry[]> {
    return store.history(this.ctx.storage);
  }

  async prune(keepN: number): Promise<PruneResult> {
    return store.prune(this.ctx.storage, keepN);
  }

  async versionStamp(): Promise<string | undefined> {
    return store.versionStamp(this.ctx.storage);
  }

  async setVersionStamp(version: string): Promise<void> {
    await store.setVersionStamp(this.ctx.storage, version);
  }
}
