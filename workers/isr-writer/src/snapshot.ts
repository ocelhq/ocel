import {
  mergeRecord,
  mergeSnapshot,
  readableSnapshot,
  tagSnapshotKey,
  type TagRecord,
  type TagSnapshot,
} from "@ocel/next-cache";

import { isRateLimited } from "./r2";

// What a publish did, as the caller has to be able to act on it. "unchanged"
// and "published" both mean R2 holds every record the caller raised; only
// "exhausted" means it does not, and it is the caller's to raise again.
// "absent" is the heartbeat's alone: there is no snapshot here to republish.
export type PublishOutcome = "published" | "unchanged" | "exhausted" | "absent";

// R2 rate-limits a single key at one write per second, so a burst is answered
// with 429 exactly when the clock is hottest. Four attempts spaced 200/400/800ms
// spans a little over a second of throttling — enough for the limit to lapse,
// short enough to answer inside a request.
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

// TagClock is one build's tag-clock replica, and the only writer of it. A
// Durable Object addressed by isrPrefix is single-threaded, so the merge happens
// in memory against the document this instance already holds and the write needs
// no compare-and-swap — which is what replaces three publishers each running
// their own three-attempt loop against the same key.
//
// Raises that arrive while a publish is in flight are coalesced into the next
// one, so a herd of invalidations on a build costs writes proportional to the
// round trip rather than to the herd.
export class TagClock {
  private readonly key: string;
  // undefined until this instance has read R2. null means the deploy seeded no
  // snapshot: the anchor is unknowable, so nothing may be pruned.
  private held: TagSnapshot | null | undefined;
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

  // Merges `records` into the build's snapshot, answering only once R2 holds the
  // result — the raiser reads its own write straight afterwards, and a snapshot
  // that has not landed yet reads as an invalidation that never happened.
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

  // Republishes the snapshot unchanged so generatedAt advances. The document
  // carries no expiry, so without this an untouched object means both "nothing
  // has changed" and "nobody has published in a week", and no reader can tell
  // the two apart.
  heartbeat(at: number): Promise<PublishOutcome> {
    return this.chain(() => this.republish(at));
  }

  // One publish at a time, so no two ever race this instance's held document.
  private chain(step: () => Promise<PublishOutcome>): Promise<PublishOutcome> {
    const done = this.draining.catch(() => undefined).then(step);
    this.draining = done;
    return done;
  }

  private async publish(records: Map<string, TagRecord>, at: number): Promise<PublishOutcome> {
    for (let attempt = 0; attempt < publishAttempts; attempt++) {
      if (attempt > 0) await backoff(attempt);
      const prior = await this.prior();
      const merged = mergeSnapshot(prior, records, at);
      // Pruning is a removal, so the record set is not monotone: a tag absent
      // from the prior document may be one that was invalidated and then proved
      // inert. Only the merged document can say whether R2 already reflects what
      // was raised, so that is what the write turns on.
      if (prior !== null && sameRecords(prior.records, merged.records)) return "unchanged";
      if (await this.put(merged, prior)) return "published";
    }
    return "exhausted";
  }

  // Never creates the object. A build with no snapshot is one the deploy never
  // seeded or the prune has taken, and a document conjured here would carry no
  // deploy anchor — so it could never prune, for a build that may not exist.
  private async republish(at: number): Promise<PublishOutcome> {
    for (let attempt = 0; attempt < publishAttempts; attempt++) {
      if (attempt > 0) await backoff(attempt);
      const prior = await this.prior();
      if (prior === null) return "absent";
      if (await this.put(mergeSnapshot(prior, noRecords, at), prior)) return "published";
    }
    return "exhausted";
  }

  private async prior(): Promise<TagSnapshot | null> {
    if (this.held === undefined) this.held = await this.read();
    return this.held;
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

  private async put(snapshot: TagSnapshot, prior: TagSnapshot | null): Promise<boolean> {
    try {
      const written = await this.bucket.put(this.key, JSON.stringify(snapshot), {
        httpMetadata: { contentType: "application/json" },
        // Nothing orders the deploy's genesis seed against the first
        // invalidation of a build, and the seed is the only writer of
        // deployedAt. Creating this object unconditionally would replace the
        // anchor it just missed with a zero, disabling pruning for the life of
        // the build; losing the race instead costs a re-read.
        ...(prior === null ? { onlyIf: { etagDoesNotMatch: "*" } } : {}),
      });
      if (written !== null) {
        this.held = snapshot;
        return true;
      }
    } catch (err) {
      if (!isRateLimited(err)) throw err;
    }
    // Either R2 refused the write or someone else got there first. Both are
    // answered by reading what is actually there and merging onto that.
    this.held = undefined;
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
