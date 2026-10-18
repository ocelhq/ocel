import { readTagRelease } from "@platform/edge-contract/cache-tags";
import type { PurgeMode } from "@platform/edge-contract/purge";
import type { BacklogStore } from "../src/purge-backlog";

interface Pending {
  mode: PurgeMode;
  raisedAt: number;
  latestAt: number;
}

interface Covered {
  purgedAt?: number;
  invalidatedAt?: number;
}

export interface MemoryBacklogStore extends BacklogStore {
  purgedTags(): string[];
}

export function memoryBacklogStore(): MemoryBacklogStore {
  const pending = new Map<string, Pending>();
  const covered = new Map<string, Covered>();
  let failures = 0;
  let retryAt: number | undefined;

  const insertionOrder = () => [...pending.entries()];

  return {
    readCoveredAt(tag, mode) {
      const row = covered.get(tag);
      if (!row) return undefined;
      if (mode === "purge") return row.purgedAt;
      const times = [row.purgedAt, row.invalidatedAt].filter((t) => t !== undefined);
      return times.length > 0 ? Math.max(...times) : undefined;
    },
    readRetryAt: () => retryAt,
    readFailures: () => failures,
    readOldestRaisedAt() {
      const times = [...pending.values()].map((row) => row.raisedAt);
      return times.length > 0 ? Math.min(...times) : undefined;
    },
    hasPending: (mode) => [...pending.values()].some((row) => row.mode === mode),
    hasAnyPending: () => pending.size > 0,
    readBatch: (mode, limit) =>
      insertionOrder()
        .filter(([, row]) => row.mode === mode)
        .sort(([, a], [, b]) => a.raisedAt - b.raisedAt)
        .slice(0, limit)
        .map(([tag]) => tag),
    readPendingReleases: () => [...new Set([...pending.keys()].map(readTagRelease))].sort(),
    addPending(mode, tag, raisedAt) {
      const row = pending.get(tag);
      if (!row) {
        pending.set(tag, { mode, raisedAt, latestAt: raisedAt });
        return;
      }
      row.mode = row.mode === "purge" || mode === "purge" ? "purge" : "invalidate";
      row.raisedAt = Math.min(row.raisedAt, raisedAt);
      row.latestAt = Math.max(row.latestAt, raisedAt);
    },
    settleBatch(mode, tags, startedAt) {
      for (const tag of tags) {
        const row = covered.get(tag) ?? {};
        if (mode === "purge") row.purgedAt = Math.max(row.purgedAt ?? 0, startedAt);
        else row.invalidatedAt = Math.max(row.invalidatedAt ?? 0, startedAt);
        covered.set(tag, row);
        const waiting = pending.get(tag);
        if (!waiting || waiting.mode !== mode) continue;
        if (waiting.latestAt <= startedAt) pending.delete(tag);
        else waiting.raisedAt = Math.max(waiting.raisedAt, startedAt);
      }
    },
    settleReleases(releases, startedAt) {
      for (const release of releases) {
        const row = covered.get(release) ?? {};
        row.purgedAt = Math.max(row.purgedAt ?? 0, startedAt);
        covered.set(release, row);
      }
      for (const [tag, row] of insertionOrder()) {
        if (!releases.includes(readTagRelease(tag))) continue;
        if (row.latestAt <= startedAt) pending.delete(tag);
        else row.raisedAt = Math.max(row.raisedAt, startedAt);
      }
    },
    writeBackoff(count, at) {
      failures = count;
      retryAt = at;
    },
    clearBackoff() {
      failures = 0;
      retryAt = undefined;
    },
    forgetCoveredBefore(at) {
      for (const [tag, row] of covered) {
        const newest = Math.max(row.purgedAt ?? 0, row.invalidatedAt ?? 0);
        if (newest < at) covered.delete(tag);
      }
    },
    purgedTags: () => [...covered.keys()].sort(),
  };
}
