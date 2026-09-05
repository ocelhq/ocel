import { DurableObject } from "cloudflare:workers";
import type { Env } from "./env";
import * as registry from "./registry";

export class IsrDeploy extends DurableObject<Env> {
  async initialize(secretHash: string): Promise<void> {
    registry.initialize(this.ctx.storage, secretHash);
  }

  async secretHash(): Promise<string | undefined> {
    return registry.secretHash(this.ctx.storage);
  }

  async destroy(): Promise<void> {
    await this.ctx.storage.deleteAll();
  }
}
