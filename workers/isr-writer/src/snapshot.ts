import {
  mergeRecord,
  mergeSnapshot,
  readableSnapshot,
  tagSnapshotKey,
  type TagRecord,
  type TagSnapshot,
} from "@ocel/next-cache";

import { isRateLimited } from "./r2";

export type PublishOutcome = "published" | "unchanged" | "exhausted" | "absent";

const publishAttempts = 4;
const retryDelayMs = 200;

function backoff(attempt: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, retryDelayMs * 2 ** (attempt - 1)));
}

function parseJson<T>(body: string): T | null {
  try {
    return JSON.parse(body) as T;
  } catch {
    return null;
  }
}

export class TagClock {
  private readonly key: string;
  private held: TagSnapshot | null = null;
  private pending = new Map<string, TagRecord>();
  private pendingAt = 0;
  private queued: Promise<PublishOutcome> | undefined;
  private draining: Promise<unknown> = Promise.resolve();

  constructor(
    private readonly bucket: R2Bucket,
    isrPrefix: string,
  ) {
    this.key = tagSnapshotKey(isrPrefix);
  }

  raise(records: Map<string, TagRecord>, at: number): Promise<PublishOutcome> {
    for (const [tag, record] of records) {
      this.pending.set(tag, mergeRecord(this.pending.get(tag), record));
    }
    this.pendingAt = Math.max(this.pendingAt, at);
    return (this.queued ??= this.chain(() => {
      const records = this.pending;
      const at = this.pendingAt;
      this.pending = new Map();
      this.queued = undefined;
      return this.publish(records, at);
    }));
  }

  heartbeat(at: number): Promise<PublishOutcome> {
    return this.chain(() => this.republish(at));
  }

  private chain(step: () => Promise<PublishOutcome>): Promise<PublishOutcome> {
    const done = this.draining.catch(() => undefined).then(step);
    this.draining = done;
    return done;
  }

  private async publish(records: Map<string, TagRecord>, at: number): Promise<PublishOutcome> {
    for (let attempt = 0; attempt < publishAttempts; attempt++) {
      if (attempt > 0) await backoff(attempt);
      const prior = await this.prior();
      if (prior === null) return "absent";
      const merged = mergeSnapshot(prior, records, at);
      if (sameRecords(prior.records, merged.records)) return "unchanged";
      if (await this.put(merged)) return "published";
    }
    return "exhausted";
  }

  private async republish(at: number): Promise<PublishOutcome> {
    for (let attempt = 0; attempt < publishAttempts; attempt++) {
      if (attempt > 0) await backoff(attempt);
      const prior = await this.prior();
      if (prior === null) return "absent";
      if (await this.put(mergeSnapshot(prior, noRecords, at))) return "published";
    }
    return "exhausted";
  }

  private async prior(): Promise<TagSnapshot | null> {
    return this.held ?? (this.held = await this.read());
  }

  private async read(): Promise<TagSnapshot | null> {
    const object = await this.bucket.get(this.key);
    if (object === null) return null;
    const snapshot = readableSnapshot(parseJson<TagSnapshot>(await object.text()));
    if (snapshot === null) {
      throw new Error(`ocel: tag snapshot at ${this.key} is not a document this writer can read`);
    }
    return snapshot;
  }

  private async put(snapshot: TagSnapshot): Promise<boolean> {
    try {
      const written = await this.bucket.put(this.key, JSON.stringify(snapshot), {
        httpMetadata: { contentType: "application/json" },
      });
      if (written !== null) {
        this.held = snapshot;
        return true;
      }
    } catch (err) {
      if (!isRateLimited(err)) throw err;
    }
    this.held = null;
    return false;
  }
}

const noRecords = new Map<string, TagRecord>();

function sameRecords(a: Record<string, TagRecord>, b: Record<string, TagRecord>): boolean {
  const tags = Object.keys(a);
  if (tags.length !== Object.keys(b).length) return false;
  return tags.every(
    (tag) =>
      Object.hasOwn(b, tag) && a[tag]!.stale === b[tag]!.stale && a[tag]!.expired === b[tag]!.expired,
  );
}
