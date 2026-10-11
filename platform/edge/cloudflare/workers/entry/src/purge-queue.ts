import { DurableObject } from "cloudflare:workers";
import type { PurgeMode } from "@platform/edge-contract/purge";
import type { Env } from "./env";
import { PurgeBacklog, type PurgeOutcome, type PurgePort, type RaisedTag } from "./purge-backlog";
import { sqliteBacklogStore } from "./purge-sqlite-store";

export interface ServePurgeRpc {
  purge(mode: PurgeMode, tags: string[]): Promise<PurgeOutcome>;
  purgeEverything(): Promise<PurgeOutcome>;
}

async function reached(call: () => Promise<PurgeOutcome>): Promise<PurgeOutcome> {
  try {
    return await call();
  } catch (error) {
    return {
      ok: false,
      codes: [],
      message: error instanceof Error ? error.message : String(error),
    };
  }
}

export function servePort(serve: () => ServePurgeRpc): PurgePort {
  return {
    purgeTags: (mode, tags) => reached(() => serve().purge(mode, tags)),
    purgeEverything: () => reached(() => serve().purgeEverything()),
  };
}

export class PurgeQueue extends DurableObject<Env> {
  serve: PurgePort;
  private readonly backlog: PurgeBacklog;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.serve = servePort(() => ctx.exports.Serve as unknown as ServePurgeRpc);
    this.backlog = new PurgeBacklog({
      store: sqliteBacklogStore(ctx.storage),
      serve: {
        purgeTags: (mode, tags) => this.serve.purgeTags(mode, tags),
        purgeEverything: () => this.serve.purgeEverything(),
      },
      now: () => Date.now(),
      random: Math.random,
    });
  }

  async raise(mode: PurgeMode, tags: RaisedTag[]): Promise<void> {
    this.backlog.raise(mode, tags);
    const next = this.backlog.nextFlushAt();
    if (next === undefined || (await this.ctx.storage.getAlarm()) !== null) return;
    await this.ctx.storage.setAlarm(next);
  }

  async alarm(): Promise<void> {
    const next = await this.backlog.flush();
    if (next !== undefined) await this.ctx.storage.setAlarm(next);
  }
}
