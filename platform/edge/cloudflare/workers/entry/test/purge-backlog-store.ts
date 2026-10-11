import type { BacklogStore, PurgeMode } from "../src/purge-backlog";

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
  let everythingAt: number | undefined;
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
    readPurgedEverythingAt: () => everythingAt,
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
        if (waiting && waiting.mode === mode && waiting.latestAt <= startedAt) pending.delete(tag);
      }
    },
    settleEverything(startedAt) {
      everythingAt = startedAt;
      for (const [tag, row] of insertionOrder()) {
        if (row.latestAt <= startedAt) pending.delete(tag);
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
