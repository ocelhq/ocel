import { DurableObject } from "cloudflare:workers";

import { claimBuild, claimedBuild } from "./build";
import { TagClock } from "./snapshot";
import type { PublishOutcome } from "./snapshot";
import type { Env } from "./env";
import type { TagRecord } from "@ocel/next-cache";

// How long a build's clock may go unpublished before it republishes itself.
// One write a minute per build is nothing against R2's one-per-second cap on
// this key, and it is what turns an untouched object from an ambiguous signal
// into a stale one that ocelhq-wvag.13 can alarm on.
export const HEARTBEAT_MS = 60_000;

// One instance per build, addressed by that build's isrPrefix, and the only
// writer of its tag-clock replica. Single-threaded is the whole point: the
// merge happens in this instance's memory, so the write it publishes needs no
// compare-and-swap and no publisher can lose a race to another.
//
// Like IsrDeploy it carries no auth logic — index.ts decides who may raise for
// which build before a call reaches here.
export class IsrSnapshot extends DurableObject<Env> {
  private clock: TagClock | undefined;

  async raise(isrPrefix: string, records: Record<string, TagRecord>): Promise<PublishOutcome> {
    if (this.clock === undefined) {
      await claimBuild(this.ctx.storage, isrPrefix);
      this.clock = new TagClock(this.env.OCEL_CACHE_STORE, isrPrefix);
    }
    const outcome = await this.clock.raise(new Map(Object.entries(records)), Date.now());
    // Slid rather than left to fire: a build publishing on its own traffic is
    // already proving it is alive, and the beat exists for the one that is not.
    await this.ctx.storage.setAlarm(Date.now() + HEARTBEAT_MS);
    return outcome;
  }

  async alarm(): Promise<void> {
    const clock = this.clock ?? (await this.reclaim());
    // Nothing to republish, and nothing to beat for: a build whose snapshot is
    // gone has been retired or pruned, and a document conjured for it would
    // carry no deploy anchor and so could never prune again.
    if (clock === null || (await clock.heartbeat(Date.now())) === "absent") return;
    await this.ctx.storage.setAlarm(Date.now() + HEARTBEAT_MS);
  }

  // Retires the build's heartbeat. The replica itself belongs to the prune, not
  // to this object.
  async destroy(): Promise<void> {
    await this.ctx.storage.deleteAlarm();
    await this.ctx.storage.deleteAll();
    this.clock = undefined;
  }

  private async reclaim(): Promise<TagClock | null> {
    const isrPrefix = await claimedBuild(this.ctx.storage);
    if (isrPrefix === undefined) return null;
    return (this.clock = new TagClock(this.env.OCEL_CACHE_STORE, isrPrefix));
  }
}
