import type { PurgeMode } from "@platform/edge-contract/purge";

export interface RaisedTag {
  tag: string;
  raisedAt: number;
}

export type PurgeOutcome = { ok: true } | { ok: false; codes: number[]; message: string };

export interface PurgePort {
  purgeTags(mode: PurgeMode, tags: string[]): Promise<PurgeOutcome>;
}

export interface BacklogStore {
  readCoveredAt(tag: string, mode: PurgeMode): number | undefined;
  readRetryAt(): number | undefined;
  readFailures(): number;
  readOldestRaisedAt(): number | undefined;
  hasPending(mode: PurgeMode): boolean;
  hasAnyPending(): boolean;
  readBatch(mode: PurgeMode, limit: number): string[];
  readPendingReleases(): string[];
  addPending(mode: PurgeMode, tag: string, raisedAt: number): void;
  settleBatch(mode: PurgeMode, tags: string[], startedAt: number): void;
  settleReleases(releases: string[], startedAt: number): void;
  writeBackoff(failures: number, retryAt: number): void;
  clearBackoff(): void;
  forgetCoveredBefore(at: number): void;
}

export interface PurgeBacklogOptions {
  store: BacklogStore;
  serve: PurgePort;
  now(): number;
  random(): number;
  warn?(message: string): void;
  onReleasesPurged?(releases: string[], at: number): void;
}

export const TAGS_PER_CALL = 100;
export const ESCALATE_AFTER_MS = 60_000;
export const FLUSH_EVERY_MS = 1_000;
export const FORGET_AFTER_MS = 3_600_000;
export const RATE_LIMITED = 1134;

const BACKOFF_BASE_MS = 1_000;
const BACKOFF_CEILING_MS = 30_000;

export function readTagRelease(tag: string): string {
  const separator = tag.indexOf("|");
  return separator === -1 ? tag : tag.slice(0, separator);
}

export class PurgeBacklog {
  private flushing: Promise<number | undefined> | undefined;

  constructor(private readonly options: PurgeBacklogOptions) {}

  raise(mode: PurgeMode, tags: readonly RaisedTag[]): void {
    const { store } = this.options;
    for (const { tag, raisedAt } of tags) {
      const coveredAt = Math.max(
        store.readCoveredAt(tag, mode) ?? Number.NEGATIVE_INFINITY,
        store.readCoveredAt(readTagRelease(tag), "purge") ?? Number.NEGATIVE_INFINITY,
      );
      if (coveredAt >= raisedAt) continue;
      store.addPending(mode, tag, raisedAt);
    }
  }

  readNextFlushAt(): number | undefined {
    const { store, now } = this.options;
    if (!store.hasAnyPending()) return undefined;
    return Math.max(store.readRetryAt() ?? 0, now() + FLUSH_EVERY_MS);
  }

  flush(): Promise<number | undefined> {
    this.flushing ??= this.run().finally(() => {
      this.flushing = undefined;
    });
    return this.flushing;
  }

  private async run(): Promise<number | undefined> {
    const { store, serve, now } = this.options;
    const startedAt = now();
    store.forgetCoveredBefore(startedAt - FORGET_AFTER_MS);

    const retryAt = store.readRetryAt();
    if (retryAt !== undefined && startedAt < retryAt) return this.readNextFlushAt();

    const oldest = store.readOldestRaisedAt();
    if (oldest === undefined) return undefined;

    if (startedAt - oldest > ESCALATE_AFTER_MS) {
      const releases = store.readPendingReleases();
      for (let i = 0; i < releases.length; i += TAGS_PER_CALL) {
        const outcome = await serve.purgeTags("purge", releases.slice(i, i + TAGS_PER_CALL));
        if (!outcome.ok) {
          this.backOff(`purge ${releases.length} release(s)`, outcome);
          return this.readNextFlushAt();
        }
      }
      store.settleReleases(releases, startedAt);
      this.options.onReleasesPurged?.(releases, startedAt);
      store.clearBackoff();
      return this.readNextFlushAt();
    }

    const mode: PurgeMode = store.hasPending("purge") ? "purge" : "invalidate";
    const tags = store.readBatch(mode, TAGS_PER_CALL);
    const outcome = await serve.purgeTags(mode, tags);
    if (outcome.ok) {
      store.settleBatch(mode, tags, startedAt);
      store.clearBackoff();
    } else {
      this.backOff(`${mode} ${tags.length} tag(s)`, outcome);
    }
    return this.readNextFlushAt();
  }

  private backOff(what: string, refusal: Extract<PurgeOutcome, { ok: false }>): void {
    const { store, now, random, warn = console.warn } = this.options;
    const failures = store.readFailures() + 1;
    const ceiling = Math.min(BACKOFF_BASE_MS * 2 ** failures, BACKOFF_CEILING_MS);
    const wait = ceiling * (0.5 + 0.5 * random());
    store.writeBackoff(failures, now() + wait);
    const limited = refusal.codes.includes(RATE_LIMITED)
      ? "the account's purge limit"
      : "a refusal";
    warn(
      `ocel: Cloudflare answered ${limited} (${refusal.message}) to ${what}; retrying in ${Math.round(wait)}ms`,
    );
  }
}
