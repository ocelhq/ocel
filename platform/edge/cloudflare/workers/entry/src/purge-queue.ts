import { DurableObject } from "cloudflare:workers";
import type { PurgeMode } from "@platform/edge-contract/purge";
import { parseStorageCoordinate } from "@platform/edge-contract/storage-key";
import type { Env } from "./env";
import { PurgeBacklog, type PurgeOutcome, type PurgePort, type RaisedTag } from "./purge-backlog";
import { readPurgeBudget } from "./purge-budget";
import { newSqliteBacklogStore } from "./purge-sqlite-store";
import {
  newSqliteTagTimes,
  type ReleaseScope,
  type TagTimes,
  type TagTimesStore,
} from "./purge-times";
import type { ReleasesBinding } from "./releases";
import { RENDER_PROPS, type ServeProps } from "./serve-key";

const flushRetryMs = 1_000;
const flushRetryCeilingMs = 60_000;

export const RETIREMENT_CHECK_EVERY_MS = 3_600_000;
export const RELEASES_CHECKED_PER_ALARM = 10;

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
  const stub = () => serve({ props: RENDER_PROPS });
  return {
    purgeTags: (mode, tags) => settlePurgeCall(() => stub().purge(mode, tags)),
  };
}

export class PurgeQueue extends DurableObject<Env> {
  serve: PurgePort;
  releases: Pick<ReleasesBinding, "readServedRecord">;
  private readonly backlog: PurgeBacklog;
  private readonly times: TagTimesStore;
  private failedFlushes = 0;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.serve = newServePort(ctx.exports.Serve);
    this.releases = env.RELEASES;
    this.times = newSqliteTagTimes(ctx.storage);
    this.backlog = new PurgeBacklog({
      store: newSqliteBacklogStore(ctx.storage),
      serve: { purgeTags: (mode, tags) => this.serve.purgeTags(mode, tags) },
      now: () => Date.now(),
      random: Math.random,
      budget: readPurgeBudget(env.OCEL_PURGE_BUDGET),
    });
  }

  async raise(scope: ReleaseScope, mode: PurgeMode, tags: RaisedTag[]): Promise<void> {
    const release = parseStorageCoordinate(scope.storageCoordinate)?.release;
    if (release === undefined) {
      throw new Error(
        `ocel: ${scope.storageCoordinate} is not the storage coordinate of a release`,
      );
    }
    this.ctx.storage.transactionSync(() => {
      this.times.record({ ...scope, release }, mode, tags);
      this.backlog.raise(mode, tags);
    });
    await this.armAlarm(await this.flushBacklog());
    if (this.readDueChecks(1).length > 0) await this.armAlarm(Date.now());
  }

  async readTimes(tags: string[]): Promise<TagTimes> {
    return this.times.read(tags);
  }

  async alarm(): Promise<void> {
    const next = await this.flushBacklog();
    await this.retireUnservedReleases();
    await this.armAlarm(next);
  }

  private async flushBacklog(): Promise<number | undefined> {
    try {
      const next = await this.backlog.flush();
      this.failedFlushes = 0;
      return next;
    } catch (error) {
      console.error("ocel: the purge queue failed to flush its backlog; trying again", error);
      return Date.now() + Math.min(flushRetryMs * 2 ** this.failedFlushes++, flushRetryCeilingMs);
    }
  }

  private async armAlarm(at: number | undefined): Promise<void> {
    if (at === undefined) return;
    const armed = await this.ctx.storage.getAlarm();
    if (armed === null || armed > at) await this.ctx.storage.setAlarm(at);
  }

  private readDueChecks(limit: number) {
    return this.times.readReleasesCheckedBefore(Date.now() - RETIREMENT_CHECK_EVERY_MS, limit);
  }

  private async retireUnservedReleases(): Promise<void> {
    for (const { release, slug, storageCoordinate } of this.readDueChecks(
      RELEASES_CHECKED_PER_ALARM,
    )) {
      try {
        const served = await this.releases.readServedRecord({ slug, storageCoordinate });
        if (served.kind === "forgotten") this.times.forget(release);
        else if (served.kind === "not-served") this.times.retire(release, Date.now());
        else this.times.markChecked(release, Date.now());
      } catch (error) {
        console.error(`ocel: cannot tell whether release ${release} is still served`, error);
      }
    }
  }
}
