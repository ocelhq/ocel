import type { TagRecord } from "@ocel/next-cache";

// The tag clock every plural cache handler consults. Its surface is the whole of
// what a handler needs to know about invalidation; the storage behind it is
// deliberately private, because a later change swaps this process-local map for
// a durable, cross-instance one without any handler changing.
//
// Local-only today: an invalidation reaches the calling instance and nowhere
// else. That is already correct for a single warm instance, which is exactly the
// lifetime of the in-memory `default` handler's entries.
export interface TagClock {
  updateTags(tags: string[], durations?: { expire?: number }): Promise<void>;
  refreshTags(): Promise<void>;
  getExpiration(tags: string[]): Promise<number>;
  areTagsExpired(tags: string[], timestamp: number): boolean;
  areTagsStale(tags: string[], timestamp: number): boolean;
  readonly hasSynced: boolean;
}

const records = new Map<string, TagRecord>();

// performance.timeOrigin + performance.now() rather than Date.now(), matching
// the clock Next's own handler stamps entry timestamps with — the two are
// compared against each other, so they have to be the same clock.
function now(): number {
  return Math.round(performance.timeOrigin + performance.now());
}

export const tagClock: TagClock = {
  // Mirrors Next's arithmetic exactly: durations mark the tag stale now and,
  // only if an expire window was given, dead at the end of it; no durations
  // means dead immediately. Fields merge, so a later expiry never erases an
  // earlier stale marker.
  async updateTags(tags, durations) {
    const at = now();
    for (const tag of tags) {
      const existing = records.get(tag) ?? {};
      records.set(
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
  },

  async refreshTags() {},

  async getExpiration(tags) {
    let latest = 0;
    for (const tag of tags) latest = Math.max(latest, records.get(tag)?.expired ?? 0);
    return latest;
  },

  areTagsExpired(tags, timestamp) {
    return tags.some((tag) => (records.get(tag)?.expired ?? 0) > timestamp);
  },

  areTagsStale(tags, timestamp) {
    return tags.some((tag) => (records.get(tag)?.stale ?? 0) > timestamp);
  },

  // Nothing to sync, so there is no window in which this instance is ignorant of
  // invalidations it should have seen.
  get hasSynced() {
    return true;
  },
};
