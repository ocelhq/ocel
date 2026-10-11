import { DurableObject } from "cloudflare:workers";
import type { PurgeMode } from "@platform/edge-contract/purge";
import type { Env } from "./env";
import { PurgeBacklog, type PurgeOutcome, type PurgePort, type RaisedTag } from "./purge-backlog";
import { newSqliteBacklogStore } from "./purge-sqlite-store";
import { ORIGIN_PURGE_SCOPE, type ServeProps } from "./serve-key";

const flushRetryMs = 1_000;
const flushRetryCeilingMs = 60_000;

export interface ServePurgeRpc {
  purge(mode: PurgeMode, tags: string[]): Promise<PurgeOutcome>;
}

async function settlePurgeCall(call: () => Promise<PurgeOutcome>): Promise<PurgeOutcome> {
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

export type ServePurgeFactory = (options: { props: ServeProps }) => ServePurgeRpc;

export function newServePort(serve: ServePurgeFactory): PurgePort {
  const stub = () => serve({ props: ORIGIN_PURGE_SCOPE });
  return {
    purgeTags: (mode, tags) => settlePurgeCall(() => stub().purge(mode, tags)),
  };
}

export class PurgeQueue extends DurableObject<Env> {
  serve: PurgePort;
  private readonly backlog: PurgeBacklog;
  private failedFlushes = 0;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.serve = newServePort(ctx.exports.Serve as unknown as ServePurgeFactory);
    this.backlog = new PurgeBacklog({
      store: newSqliteBacklogStore(ctx.storage),
      serve: {
        purgeTags: (mode, tags) => this.serve.purgeTags(mode, tags),
      },
      now: () => Date.now(),
      random: Math.random,
    });
  }

  async raise(mode: PurgeMode, tags: RaisedTag[]): Promise<void> {
    this.backlog.raise(mode, tags);
    const next = this.backlog.readNextFlushAt();
    if (next === undefined || (await this.ctx.storage.getAlarm()) !== null) return;
    await this.ctx.storage.setAlarm(next);
  }

  async alarm(): Promise<void> {
    let next: number | undefined;
    try {
      next = await this.backlog.flush();
      this.failedFlushes = 0;
    } catch (error) {
      console.error("ocel: the purge queue failed to flush its backlog; trying again", error);
      next = Date.now() + Math.min(flushRetryMs * 2 ** this.failedFlushes++, flushRetryCeilingMs);
    }
    if (next !== undefined) await this.ctx.storage.setAlarm(next);
  }
}
