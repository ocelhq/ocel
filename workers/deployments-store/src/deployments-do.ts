import { DurableObject } from "cloudflare:workers";

import { matchesSecret } from "@ocel/worker-auth";

import * as store from "./store";
import type {
  DeploymentRecord,
  HistoryEntry,
  Identity,
  Promotion,
  PruneResult,
} from "./store";
import type { Env } from "./env";

export class DeploymentsStore extends DurableObject<Env> {
  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    store.ensureSchema(ctx.storage);
  }

  async initialize(
    ownerToken: string,
    secret: string,
    force: boolean,
  ): Promise<Identity> {
    return store.initialize(this.ctx.storage, ownerToken, secret, force);
  }

  async authorized(token: string): Promise<boolean> {
    const secret = store.storedSecret(this.ctx.storage);
    if (secret === undefined) return false;
    return matchesSecret(token, secret);
  }

  async destroy(): Promise<void> {
    await this.ctx.storage.deleteAll();
    store.ensureSchema(this.ctx.storage);
  }

  async putStaged(record: DeploymentRecord): Promise<void> {
    store.putStaged(this.ctx.storage, record);
  }

  async promote(
    promotion: Promotion,
    pointer?: string,
  ): Promise<{ conflict?: string }> {
    try {
      store.promote(this.ctx.storage, promotion, pointer);
      return {};
    } catch (e) {
      if (e instanceof store.TagConflictError) return { conflict: e.message };
      throw e;
    }
  }

  async pointerBuildId(
    app: string,
    pointer?: string,
  ): Promise<string | undefined> {
    return store.pointerBuildId(this.ctx.storage, app, pointer);
  }

  async record(
    app: string,
    buildId: string,
  ): Promise<DeploymentRecord | undefined> {
    return store.record(this.ctx.storage, app, buildId);
  }

  async pointerRecord(
    app: string,
    pointer?: string,
    knownBuildId?: string,
  ): Promise<store.PointerRecordResult> {
    return store.pointerRecord(this.ctx.storage, app, pointer, knownBuildId);
  }

  async history(pointer?: string): Promise<HistoryEntry[]> {
    return store.history(this.ctx.storage, pointer);
  }

  async prune(keepN: number, pointer?: string): Promise<PruneResult> {
    return store.prune(this.ctx.storage, keepN, pointer);
  }

  async removePointer(pointer?: string): Promise<PruneResult> {
    return store.removePointer(this.ctx.storage, pointer);
  }

  async versionStamp(): Promise<string | undefined> {
    return store.versionStamp(this.ctx.storage);
  }

  async setVersionStamp(version: string): Promise<void> {
    store.setVersionStamp(this.ctx.storage, version);
  }
}
