import { DurableObject } from "cloudflare:workers";

import { matchesSecret } from "./auth";
import type { Env } from "./env";
import type {
  Initialization,
  PointerMove,
  PointerMoveOutcome,
  PointerRecordResult,
  ServedRecordResult,
} from "./store";
import * as store from "./store";

export class ReleasesStore extends DurableObject<Env> {
  #schemaReady = false;

  private get storage(): DurableObjectStorage {
    if (!this.#schemaReady) {
      store.ensureSchema(this.ctx.storage);
      this.#schemaReady = true;
    }
    return this.ctx.storage;
  }

  async initialize(secret: string): Promise<Initialization> {
    return store.initialize(this.storage, secret);
  }

  async authorized(token: string): Promise<boolean> {
    const secret = store.readSecret(this.ctx.storage);
    if (secret === undefined) return false;
    return matchesSecret(token, secret);
  }

  async destroy(): Promise<void> {
    await this.ctx.storage.deleteAll();
    this.#schemaReady = false;
  }

  async movePointer(move: PointerMove): Promise<PointerMoveOutcome> {
    return store.movePointer(this.storage, move);
  }

  async readServedPromotion(pointer?: string): Promise<string | undefined> {
    return store.readServedPromotion(this.storage, pointer);
  }

  async removePointer(pointer: string): Promise<void> {
    store.removePointer(this.storage, pointer);
  }

  async listApps(): Promise<string[]> {
    return store.listApps(this.storage);
  }

  async readPointerRecord(app?: string, knownRelease?: string): Promise<PointerRecordResult> {
    return store.readPointerRecord(this.storage, app, knownRelease);
  }

  async readLabelRecord(label: string, knownRelease?: string): Promise<PointerRecordResult> {
    return store.readLabelRecord(this.storage, label, knownRelease);
  }

  async readServedRecord(isrPrefix: string): Promise<ServedRecordResult> {
    return store.readServedRecord(this.storage, isrPrefix);
  }

  async readVersionStamp(): Promise<string | undefined> {
    return store.readVersionStamp(this.storage);
  }

  async setVersionStamp(version: string): Promise<void> {
    store.setVersionStamp(this.storage, version);
  }

  async readGate(): Promise<string | undefined> {
    return store.readGate(this.storage);
  }

  async setGate(gate: string | undefined): Promise<void> {
    store.setGate(this.storage, gate);
  }
}
