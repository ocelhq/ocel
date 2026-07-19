import type { TagRecord } from "@ocel/next-cache";
import { awsUseCacheStore, type UseCacheStore } from "./use-cache-store.mjs";
import { mergeRecord, publishTagSnapshot, snapshotRefreshMs } from "./tag-snapshot.mjs";
import { now } from "./use-cache-entry.mjs";

// Invalidations are recorded in the state table and synced back into a local
// map, so a revalidateTag raised on one instance reaches every other instance
// within a sync interval. The local map is what answers reads: the index is
// eventually consistent, and no read may go to the network.
export interface TagClock {
  updateTags(tags: string[], durations?: { expire?: number }): Promise<void>;
  refreshTags(): Promise<void>;
  getExpiration(tags: string[]): Promise<number>;
  areTagsExpired(tags: string[], timestamp: number): boolean;
  areTagsStale(tags: string[], timestamp: number): boolean;
  readonly hasSynced: boolean;
}

interface ClockState {
  fingerprint: string;
  records: Map<string, TagRecord>;
  // undefined: not yet bound. null: no durable backend, so the clock is local.
  store: UseCacheStore | null | undefined;
  // The write time of the last record consumed. null until a sync has completed,
  // which is what marks the instance cold — and is not the same as a cursor of 0.
  cursor: number | null;
  hasSynced: boolean;
  lastAttemptAt: number;
  inflight: Promise<void> | null;
  // Bumped whenever a record actually moves. Comparing it against what was last
  // published is what lets an idle instance skip the snapshot read entirely,
  // instead of paying a round trip every sync interval to discover nothing
  // changed.
  revision: number;
  publishedRevision: number;
  publishedAt: number;
}

// Throttled on the *attempt* rather than the success, so a persistently failing
// table sees a bounded retry rate instead of one query per request.
const syncIntervalMs = 2_000;

// Versioned: a later change to the state's shape must not be adopted by a module
// compiled against this one.
const stateKey = Symbol.for("ocel.use-cache.tag-clock.v1");

// Next loads each registered handler as its own module graph, so a clock bundled
// into both handler bundles exists twice. Sharing it through globalThis is what
// keeps the two copies agreeing on one tag map, one cursor and one query — the
// same technique Next uses for its own handler registry.
// The only place a ClockState's fields are enumerated, so resetting one can
// never fall behind adding one.
function initialState(fingerprint: string): ClockState {
  return {
    fingerprint,
    records: new Map(),
    store: undefined,
    cursor: null,
    hasSynced: false,
    lastAttemptAt: -Infinity,
    inflight: null,
    revision: 0,
    publishedRevision: 0,
    publishedAt: -Infinity,
  };
}

function sharedState(): ClockState {
  const fingerprint = [
    process.env.OCEL_STATE_TABLE,
    process.env.OCEL_ISR_TAG_NAMESPACE,
    process.env.OCEL_STATE_TABLE_INDEX,
  ].join("\0");

  const host = globalThis as Record<symbol, ClockState | undefined>;
  const existing = host[stateKey];
  // An instance built from different configuration reads a different namespace,
  // so adopting it would silently answer with another deployment's tags.
  if (existing?.fingerprint === fingerprint) return existing;

  return (host[stateKey] = initialState(fingerprint));
}

const state = sharedState();

// Bound lazily so importing this module never reaches for AWS or its env. A
// store that cannot be built leaves the clock local-only, which is what lets the
// handlers ship ahead of the index they query.
//
// The binding lives with the shared clock state rather than in each handler,
// because the two handler bundles and the clock are one instance's worth of
// backend: one pair of clients, and one seam for tests to rebind.
export function useCacheStore(): UseCacheStore | null {
  if (state.store === undefined) {
    try {
      state.store = awsUseCacheStore();
    } catch {
      state.store = null;
    }
  }
  return state.store;
}

// Rebinds the shared clock, discarding the state that belonged to the previous
// store — a tag map and cursor only mean anything against the backend they came
// from. Production never calls this; tests drive the real clock against a fake.
//
// Assigned in place rather than rebound, because every module copy that has
// already read the shared state holds this exact object.
export function setTagClockStore(next: UseCacheStore | null): void {
  Object.assign(state, initialState(state.fingerprint), { store: next });
}

// Records the tag's new state and reports whether anything actually moved, which
// is what the publisher reads as "there is something new to replicate". Every
// steady-state sync re-reads the record on the cursor boundary, so counting
// those as changes would republish every sync interval forever.
function set(tag: string, record: TagRecord): void {
  const existing = state.records.get(tag);
  if (existing?.stale !== record.stale || existing?.expired !== record.expired) {
    state.revision++;
  }
  state.records.set(tag, record);
}

// Merged rather than replaced, and always upwards: a record arriving from the
// index must never walk back an invalidation this instance raised itself, which
// the index is too lagged to have seen yet.
function observe(tag: string, incoming: TagRecord): void {
  set(tag, mergeRecord(state.records.get(tag), incoming));
}

async function sync(): Promise<void> {
  const backend = useCacheStore();
  if (!backend) return;

  try {
    const cold = state.cursor === null;
    const since = state.cursor ?? 0;
    let consumed = since;
    let cursor: unknown;

    do {
      const page = await backend.queryTagRecords(since, cursor);
      for (const record of page.records) {
        observe(record.tag, record);
        consumed = Math.max(consumed, record.writtenAt);
      }
      cursor = page.cursor;
      // A cold instance drains the partition, so it knows the full invalidation
      // history for its deployment before it serves anything. In steady state a
      // truncated page advances the cursor only as far as it actually read, so
      // the next sync resumes rather than skipping the remainder.
    } while (cold && cursor);

    state.cursor = consumed;
    state.hasSynced = true;
  } catch {
    // Next does not guard refreshTags, so a throw here fails the request. A
    // state table outage — or an index that does not exist yet — leaves the
    // handlers serving on their last known tag state instead, with the cursor
    // and hasSynced left exactly where they were so nothing is skipped.
  }

  // Published even when the query failed, because the local map is what is being
  // replicated and it is already merged: recordAndPublish merges its record
  // before draining, so bailing out here would drop the very invalidation the
  // kick exists to replicate — and on an app with no `use cache` anywhere,
  // nothing ever runs this clock again to repair it.
  await publish(backend);
}

// Starts a drain and records it as the one in flight, which is what lets a
// concurrent caller join it rather than run a second query against the same
// cursor. The attempt is marked before it runs rather than after it settles,
// which is what makes the throttle above bound attempts.
function drain(): Promise<void> {
  state.lastAttemptAt = now();
  return (state.inflight = sync().finally(() => {
    state.inflight = null;
  }));
}

// Publishing from the sync drain rather than from updateTags is what makes the
// replica whole: updateTags knows only its own event, while the drain holds
// every invalidation this instance has merged from the index.
async function publish(backend: UseCacheStore): Promise<void> {
  const revision = state.revision;
  const stale = now() - state.publishedAt >= snapshotRefreshMs;
  if (revision === state.publishedRevision && !stale) return;

  try {
    // Only a confirmed write advances the marks, so a failed publish is retried
    // by the next sync rather than mistaken for a live replica.
    if (!(await publishTagSnapshot(backend, state.records))) return;
    state.publishedRevision = revision;
    state.publishedAt = now();
  } catch {
    // The replica is an optimization; DynamoDB remains the authoritative clock
    // and the origin is still correct. A publish that cannot land costs the edge
    // its trust window, which falls open and wakes a publisher.
  }
}

// The singular handler makes its own durable tag write, so on an app with no
// `use cache` anywhere nothing else ever calls into this clock — and a drain
// that never runs is a replica that never hears about the invalidation. This is
// the kick that wakes it, deliberately outside the read path's throttle: it
// answers one specific event, and throttling it would drop that event.
//
// The record is merged locally before the drain because the index is eventually
// consistent. Publishing only what the query read back would produce a snapshot
// missing the very invalidation that woke the publisher.
export async function recordAndPublish(
  tags: string[],
  record: TagRecord,
): Promise<void> {
  for (const tag of tags) observe(tag, record);

  // Joined rather than raced: a sync already in flight was started before this
  // write and holds the cursor, so a second one alongside it would advance the
  // cursor past records neither had read.
  if (state.inflight) await state.inflight;

  await drain();
}

export const tagClock: TagClock = {
  // Mirrors Next's arithmetic exactly: durations mark the tag stale now and,
  // only if an expire window was given, dead at the end of it; no durations
  // means dead immediately. Fields merge, so a later expiry never erases an
  // earlier stale marker.
  //
  // The local map is updated synchronously, before the durable write, because
  // that is what gives the raising instance read-your-own-writes across an
  // eventually consistent index.
  async updateTags(tags, durations) {
    const at = now();
    for (const tag of tags) {
      const existing = state.records.get(tag) ?? {};
      set(
        tag,
        durations
          ? {
              ...existing,
              stale: at,
              ...(durations.expire !== undefined
                ? { expired: at + durations.expire * 1000 }
                : {}),
            }
          : { ...existing, expired: at },
      );
    }

    const backend = useCacheStore();
    if (!backend) return;

    await Promise.all(
      tags.map(async (tag) => {
        const record = state.records.get(tag)!;
        try {
          // The outcome is deliberately unused: a rejected write means another
          // writer already recorded a stricter invalidation for this event.
          await backend.writeTag(tag, { ...record, writtenAt: at });
        } catch {
          // The invalidation is already local; a durable write that fails costs
          // other instances a sync interval, not this request.
        }
      }),
    );
  },

  async refreshTags() {
    if (state.inflight) return state.inflight;
    if (now() - state.lastAttemptAt < syncIntervalMs) return;

    return drain();
  },

  async getExpiration(tags) {
    let expiration = 0;
    for (const tag of tags) {
      expiration = Math.max(expiration, state.records.get(tag)?.expired ?? 0);
    }
    return expiration;
  },

  areTagsExpired(tags, timestamp) {
    return tags.some((tag) => (state.records.get(tag)?.expired ?? 0) > timestamp);
  },

  areTagsStale(tags, timestamp) {
    return tags.some((tag) => (state.records.get(tag)?.stale ?? 0) > timestamp);
  },

  // Distinct from "synced but stale": an empty map on an instance that has never
  // synced means "I know nothing about invalidations", which a handler must not
  // read as "nothing was invalidated".
  get hasSynced() {
    return state.hasSynced;
  },
};

// The three CacheHandler methods that are pure clock delegation, shared so both
// tiers cannot drift apart. Next fans updateTags out to every registered
// handler, so both raise every invalidation; the shared clock collapses them,
// the second durable write losing the monotonic guard.
export const clockMethods = {
  async refreshTags(): Promise<void> {
    await tagClock.refreshTags();
  },

  async getExpiration(tags: string[]): Promise<number> {
    return tagClock.getExpiration(tags);
  },

  async updateTags(tags: string[], durations?: { expire?: number }): Promise<void> {
    await tagClock.updateTags(tags, durations);
  },
};
