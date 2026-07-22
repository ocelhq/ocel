import { DurableObject } from "cloudflare:workers";

import { constantTimeEqual } from "./auth";
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
  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    store.ensureSchema(ctx.storage);
  }

  // Seeds or rotates this instance's ownership token and project secret. Like
  // promote(), it returns { conflict } rather than throwing so an ownership
  // collision survives the RPC boundary: index.ts turns it into a 409.
  async initialize(
    ownerToken: string,
    secret: string,
    force: boolean,
  ): Promise<{ conflict?: string }> {
    try {
      store.initialize(this.ctx.storage, ownerToken, secret, force);
      return {};
    } catch (e) {
      if (e instanceof store.OwnershipConflictError) return { conflict: e.message };
      throw e;
    }
  }

  // Constant-time-compares a bearer token against this instance's own stored
  // project secret. False before the instance is initialized (no secret yet),
  // so every op but initialize is rejected until the project is seeded.
  async authorized(token: string): Promise<boolean> {
    const secret = store.storedSecret(this.ctx.storage);
    if (secret === undefined) return false;
    return constantTimeEqual(token, secret);
  }

  // Clears the instance's storage — history, records, ownership and secret —
  // then re-creates the empty schema so the slug is immediately reusable by a
  // fresh project. The shared worker itself is never deleted here (ocel
  // destroy tears down only the project's own instance and app workers).
  async destroy(): Promise<void> {
    await this.ctx.storage.deleteAll();
    store.ensureSchema(this.ctx.storage);
  }

  async putStaged(record: DeploymentRecord): Promise<void> {
    store.putStaged(this.ctx.storage, record);
  }

  // Returns { conflict } instead of throwing so the tag-collision signal
  // survives the RPC boundary (custom error prototypes do not): index.ts turns
  // a conflict into a 409 the deploy host surfaces verbatim.
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

  async versionStamp(): Promise<string | undefined> {
    return store.versionStamp(this.ctx.storage);
  }

  async setVersionStamp(version: string): Promise<void> {
    store.setVersionStamp(this.ctx.storage, version);
  }
}
