import { DurableObject } from "cloudflare:workers";

import { matchesSecret } from "@platform/cf-auth";
import type { Env } from "./env";
import type { Flip, Flipped, Initialization, PointerRecordResult } from "./store";
import * as store from "./store";

export class DeploymentsStore extends DurableObject<Env> {
  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    store.ensureSchema(ctx.storage);
  }

  async initialize(ownerToken: string, secret: string, force: boolean): Promise<Initialization> {
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

  async flip(flipped: Flip): Promise<Flipped> {
    return store.flip(this.ctx.storage, flipped);
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

  async pointerRecord(
    app?: string,
    pointer?: string,
    knownIdentity?: string,
  ): Promise<PointerRecordResult> {
    return store.pointerRecord(this.ctx.storage, app, pointer, knownIdentity);
  }

  async versionStamp(): Promise<string | undefined> {
    return store.versionStamp(this.ctx.storage);
  }

  async setVersionStamp(version: string): Promise<void> {
    store.setVersionStamp(this.ctx.storage, version);
  }
}
