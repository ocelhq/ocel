import { DurableObject } from "cloudflare:workers";

import { claimBuild, claimedBuild } from "./build";
import { TagClock } from "./snapshot";
import type { PublishOutcome } from "./snapshot";
import type { Env } from "./env";
import type { TagRecord } from "@framework/next-cache";

export const HEARTBEAT_MS = 60_000;

export class IsrSnapshot extends DurableObject<Env> {
  private clock: TagClock | undefined;

  async begin(isrPrefix: string): Promise<void> {
    await this.claim(isrPrefix);
    await this.ctx.storage.setAlarm(Date.now() + HEARTBEAT_MS);
  }

  async raise(isrPrefix: string, records: Record<string, TagRecord>): Promise<PublishOutcome> {
    const clock = await this.claim(isrPrefix);
    const outcome = await clock.raise(new Map(Object.entries(records)), Date.now());
    await this.ctx.storage.setAlarm(Date.now() + HEARTBEAT_MS);
    return outcome;
  }

  async alarm(): Promise<void> {
    const clock = this.clock ?? (await this.reclaim());
    if (clock === null || (await clock.heartbeat(Date.now())) === "absent") return;
    await this.ctx.storage.setAlarm(Date.now() + HEARTBEAT_MS);
  }

  async destroy(): Promise<void> {
    await this.ctx.storage.deleteAlarm();
    await this.ctx.storage.deleteAll();
    this.clock = undefined;
  }

  private async claim(isrPrefix: string): Promise<TagClock> {
    if (this.clock === undefined) {
      await claimBuild(this.ctx.storage, isrPrefix);
      this.clock = new TagClock(this.env.OCEL_CACHE_STORE, isrPrefix);
    }
    return this.clock;
  }

  private async reclaim(): Promise<TagClock | null> {
    const isrPrefix = await claimedBuild(this.ctx.storage);
    if (isrPrefix === undefined) return null;
    return (this.clock = new TagClock(this.env.OCEL_CACHE_STORE, isrPrefix));
  }
}
