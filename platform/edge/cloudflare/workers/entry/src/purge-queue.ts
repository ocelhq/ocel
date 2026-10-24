import { DurableObject } from "cloudflare:workers";
import type { PurgeMode } from "@platform/edge-contract/purge";
import type { Env } from "./env";
import {
  type BacklogStore,
  PurgeBacklog,
  type PurgeOutcome,
  type PurgePort,
  type RaisedTag,
} from "./purge-backlog";
import { openPurgeQueue, PURGE_QUEUE_NAME } from "./purge-request";
import { newSqliteBacklogStore } from "./purge-sqlite-store";
import {
  newSqliteTagTimes,
  TAG_TIMES_RETENTION_MS,
  type TagTimes,
  type TagTimesStore,
} from "./purge-times";
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

export interface TimesRecorder {
  record(mode: PurgeMode, tags: RaisedTag[]): Promise<void>;
}

const RECORDED_ON_RAISE: TimesRecorder = { record: async () => {} };

export type ServePurgeFactory = (options: { props: ServeProps }) => ServePurgeRpc;

export function newServePort(serve: ServePurgeFactory): PurgePort {
  const stub = () => serve({ props: ORIGIN_PURGE_SCOPE });
  return {
    purgeTags: (mode, tags) => settlePurgeCall(() => stub().purge(mode, tags)),
  };
}

export class PurgeQueue extends DurableObject<Env> {
  serve: PurgePort;
  recorder: TimesRecorder;
  private readonly backlog: PurgeBacklog;
  private readonly pending: BacklogStore;
  private readonly times: TagTimesStore;
  private failedFlushes = 0;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.serve = newServePort(ctx.exports.Serve as unknown as ServePurgeFactory);
    this.times = newSqliteTagTimes(ctx.storage);
    const queues = (ctx.exports as unknown as { PurgeQueue: DurableObjectNamespace }).PurgeQueue;
    this.recorder = ctx.id.equals(queues.idFromName(PURGE_QUEUE_NAME))
      ? RECORDED_ON_RAISE
      : openPurgeQueue(ctx.exports);
    this.pending = newSqliteBacklogStore(ctx.storage);
    this.backlog = new PurgeBacklog({
      store: this.pending,
      serve: {
        purgeTags: (mode, tags) => this.purgeRecorded(mode, tags),
      },
      now: () => Date.now(),
      random: Math.random,
      onReleasesPurged: (releases, at) =>
        this.times.record(
          "purge",
          releases.map((tag) => ({ tag, raisedAt: at })),
        ),
    });
  }

  private async purgeRecorded(mode: PurgeMode, tags: string[]): Promise<PurgeOutcome> {
    const raisedAt = Date.now();
    const expires = this.pending.readExpiresAt(tags);
    try {
      await this.recorder.record(
        mode,
        tags.map((tag) => ({
          tag,
          raisedAt,
          ...(expires[tag] !== undefined && { expiresAt: expires[tag] }),
        })),
      );
    } catch (error) {
      return {
        ok: false,
        codes: [],
        message: `the purge queue kept no time: ${error instanceof Error ? error.message : String(error)}`,
      };
    }
    return this.serve.purgeTags(mode, tags);
  }

  async record(mode: PurgeMode, tags: RaisedTag[]): Promise<void> {
    this.times.forgetBefore(Date.now() - TAG_TIMES_RETENTION_MS);
    this.times.record(mode, tags);
  }

  async readTimes(tags: string[]): Promise<TagTimes> {
    return this.times.read(tags);
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
