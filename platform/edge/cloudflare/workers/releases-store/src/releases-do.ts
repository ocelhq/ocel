import { DurableObject } from "cloudflare:workers";

import { matchesSecret } from "@platform/cf-auth";
import type { Env } from "./env";
import { ROUTE_TABLE_DELETE_MAX } from "./route-table";
import type { Initialization, PointerMove, PointerMoveOutcome, PointerRecordResult } from "./store";
import * as store from "./store";

export class ReleasesStore extends DurableObject<Env> {
  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    store.ensureSchema(ctx.storage);
  }

  async initialize(ownerToken: string, secret: string, force: boolean): Promise<Initialization> {
    return store.initialize(this.ctx.storage, ownerToken, secret, force);
  }

  async authorized(token: string): Promise<boolean> {
    const secret = store.readSecret(this.ctx.storage);
    if (secret === undefined) return false;
    return matchesSecret(token, secret);
  }

  async destroy(): Promise<void> {
    const keys = store.listRouteTables(this.ctx.storage);
    for (let i = 0; i < keys.length; i += ROUTE_TABLE_DELETE_MAX) {
      await this.env.OCEL_CACHE_STORE.delete(keys.slice(i, i + ROUTE_TABLE_DELETE_MAX));
    }
    await this.ctx.storage.deleteAll();
    store.ensureSchema(this.ctx.storage);
  }

  async recordRouteTable(key: string): Promise<void> {
    store.recordRouteTable(this.ctx.storage, key);
  }

  async forgetRouteTable(key: string): Promise<void> {
    store.forgetRouteTable(this.ctx.storage, key);
  }

  async movePointer(move: PointerMove): Promise<PointerMoveOutcome> {
    return store.movePointer(this.ctx.storage, move);
  }

  async readServedPromotion(pointer?: string): Promise<string | undefined> {
    return store.readServedPromotion(this.ctx.storage, pointer);
  }

  async removePointer(pointer: string): Promise<void> {
    store.removePointer(this.ctx.storage, pointer);
  }

  async listApps(): Promise<string[]> {
    return store.listApps(this.ctx.storage);
  }

  async readPointerRecord(app?: string, knownRelease?: string): Promise<PointerRecordResult> {
    return store.readPointerRecord(this.ctx.storage, app, knownRelease);
  }

  async readLabelRecord(label: string, knownRelease?: string): Promise<PointerRecordResult> {
    return store.readLabelRecord(this.ctx.storage, label, knownRelease);
  }

  async readVersionStamp(): Promise<string | undefined> {
    return store.readVersionStamp(this.ctx.storage);
  }

  async setVersionStamp(version: string): Promise<void> {
    store.setVersionStamp(this.ctx.storage, version);
  }
}
