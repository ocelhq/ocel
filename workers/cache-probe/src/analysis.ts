// Aggregation over what the probe observed. Every elapsed time here is measured
// by the runner against its own clock, never by differencing timestamps taken in
// two isolates — Date.now() in a Worker only advances on I/O, so cross-isolate
// arithmetic on it would be measuring the runtime, not the cache.

export interface IdentitySample {
  colo: string;
  isolate: string;
}

export interface IsolateSummary {
  colo: string;
  isolates: number;
  samples: number;
}

export function summarizeIsolates(samples: IdentitySample[]): IsolateSummary[] {
  const byColo = new Map<string, Set<string>>();
  const counts = new Map<string, number>();
  for (const { colo, isolate } of samples) {
    let isolates = byColo.get(colo);
    if (!isolates) {
      isolates = new Set();
      byColo.set(colo, isolates);
    }
    isolates.add(isolate);
    counts.set(colo, (counts.get(colo) ?? 0) + 1);
  }
  return [...byColo.entries()]
    .map(([colo, isolates]) => ({
      colo,
      isolates: isolates.size,
      samples: counts.get(colo)!,
    }))
    .sort((a, b) => a.colo.localeCompare(b.colo));
}

export interface SentinelObservation {
  reader: string;
  colo: string;
  writer: string | null;
  hit: boolean;
  elapsedMs: number;
}

export type SentinelVerdict =
  // Nearly every cross-isolate read hit: L1 as designed is viable.
  | "cross-isolate-visible"
  // Some cross-isolate reads hit and some missed. A colo is many machines, so
  // this is the expected shape of a real sharing result. L1 suppresses only the
  // observed fraction, and L2 must be sized by that suppression factor rather
  // than by colo count.
  | "partially-visible"
  // Other isolates read and missed while the writer's own isolate hit: L1 is a
  // no-op and L2 must be sized for isolate-count fan-in.
  | "isolate-local"
  // Nothing ever hit, including the writer's isolate: the cache is inert here
  // (the workers.dev case), so the run measured nothing about isolates.
  | "never-cached"
  // No read ever landed on an isolate other than the writer's, so the run
  // cannot distinguish the outcomes.
  | "inconclusive";

// A cross-isolate hit rate at or above this reads as full sharing rather than
// partial. Below it the suppression factor, not the verdict, is the finding.
const FULLY_VISIBLE_HIT_RATE = 0.9;

export interface SentinelSummary {
  verdict: SentinelVerdict;
  readerIsolates: number;
  crossIsolateReads: number;
  crossIsolateHits: number;
  crossIsolateHitRate: number | null;
  writerIsolateReads: number;
  writerIsolateHits: number;
  firstCrossIsolateHitMs: number | null;
  foreignColoObservations: number;
}

export function summarizeSentinel(
  writer: string,
  writerColo: string | null,
  observations: SentinelObservation[],
): SentinelSummary {
  const sameColo = writerColo
    ? observations.filter((o) => o.colo === writerColo)
    : observations;

  const cross = sameColo.filter((o) => o.reader !== writer);
  const crossHits = cross.filter((o) => o.hit);
  const own = sameColo.filter((o) => o.reader === writer);

  const summary = {
    readerIsolates: new Set(sameColo.map((o) => o.reader)).size,
    crossIsolateReads: cross.length,
    crossIsolateHits: crossHits.length,
    crossIsolateHitRate: cross.length ? crossHits.length / cross.length : null,
    writerIsolateReads: own.length,
    writerIsolateHits: own.filter((o) => o.hit).length,
    firstCrossIsolateHitMs: crossHits.length
      ? Math.min(...crossHits.map((o) => o.elapsedMs))
      : null,
    foreignColoObservations: observations.length - sameColo.length,
  };

  return { ...summary, verdict: sentinelVerdict(summary) };
}

function sentinelVerdict(summary: Omit<SentinelSummary, "verdict">): SentinelVerdict {
  if (summary.crossIsolateReads === 0) return "inconclusive";
  if (summary.crossIsolateHits === 0) {
    return summary.writerIsolateHits > 0 ? "isolate-local" : "never-cached";
  }
  return summary.crossIsolateHitRate! >= FULLY_VISIBLE_HIT_RATE
    ? "cross-isolate-visible"
    : "partially-visible";
}

export const showsCrossIsolateVisibility = (verdict: SentinelVerdict) =>
  verdict === "cross-isolate-visible" || verdict === "partially-visible";

export interface TtlRead {
  isolate: string;
  hit: boolean;
  ageSeconds: number | null;
  cacheControl: string | null;
}

export interface TtlObservation {
  elapsedMs: number;
  reads: TtlRead[];
}

export type TtlVerdict =
  // A hit was observed at or past the requested TTL.
  | "honored"
  // The entry was already gone before the requested TTL elapsed.
  | "evicted-early"
  // The observed lifetime brackets the requested TTL, or no poll was able to
  // observe the entry's liveness at all, so the run proves neither.
  | "indeterminate"
  | "never-cached";

export interface TtlSummary {
  requestedTtlSeconds: number;
  verdict: TtlVerdict;
  lastHitMs: number | null;
  firstMissAfterLastHitMs: number | null;
  // True when polling stopped while the entry was still live, so its lifetime is
  // only bounded from below. An entry that outlives its TTL by a wide margin is
  // the signal that Cloudflare floored the TTL upward.
  stillLiveAtEndOfWindow: boolean;
  // Misses observed before a later hit — a read that missed while the entry was
  // demonstrably still live, which is itself worth seeing.
  transientMisses: number;
  polls: number;
  // Polls whose reads could actually observe the entry: all of them when the
  // sentinel phase proved the cache is shared, otherwise only those that landed
  // on the writer's own isolate. A poll that could not observe the entry says
  // nothing about its lifetime, and counting it would manufacture a confident
  // evicted-early out of routing luck.
  authoritativePolls: number;
  // Cloudflare's own account of the entry, independent of this probe's polling
  // luck: the largest age it reported bounds the lifetime from below, and a
  // cache-control differing from what was written answers whether cache.put
  // rewrites TTLs.
  maxObservedAgeSeconds: number | null;
  observedCacheControl: string[];
}

export function summarizeTtl(
  requestedTtlSeconds: number,
  writer: string,
  observations: TtlObservation[],
  crossIsolateVisible: boolean,
): TtlSummary {
  const polls = observations
    .map(({ elapsedMs, reads }) => {
      const counted = crossIsolateVisible
        ? reads
        : reads.filter((r) => r.isolate === writer);
      return counted.length ? { elapsedMs, hit: counted.some((r) => r.hit) } : null;
    })
    .filter((poll) => poll !== null)
    .sort((a, b) => a.elapsedMs - b.elapsedMs);

  const hits = polls.filter((p) => p.hit);
  const lastHitMs = hits.length ? hits[hits.length - 1]!.elapsedMs : null;
  const firstMissAfterLastHitMs =
    lastHitMs === null
      ? null
      : (polls.find((p) => !p.hit && p.elapsedMs > lastHitMs)?.elapsedMs ?? null);

  const hitReads = observations.flatMap((o) => o.reads).filter((r) => r.hit);
  const ages = hitReads.map((r) => r.ageSeconds).filter((age) => age !== null);

  return {
    requestedTtlSeconds,
    lastHitMs,
    firstMissAfterLastHitMs,
    stillLiveAtEndOfWindow: lastHitMs !== null && firstMissAfterLastHitMs === null,
    transientMisses:
      lastHitMs === null
        ? 0
        : polls.filter((p) => !p.hit && p.elapsedMs < lastHitMs).length,
    polls: observations.length,
    authoritativePolls: polls.length,
    maxObservedAgeSeconds: ages.length ? Math.max(...ages) : null,
    observedCacheControl: [
      ...new Set(hitReads.map((r) => r.cacheControl).filter((cc) => cc !== null)),
    ].sort(),
    verdict:
      polls.length === 0
        ? "indeterminate"
        : ttlVerdict(requestedTtlSeconds * 1_000, lastHitMs, firstMissAfterLastHitMs),
  };
}

function ttlVerdict(
  requestedMs: number,
  lastHitMs: number | null,
  firstMissMs: number | null,
): TtlVerdict {
  if (lastHitMs === null) return "never-cached";
  if (lastHitMs >= requestedMs) return "honored";
  if (firstMissMs === null) return "indeterminate";
  if (firstMissMs <= requestedMs) return "evicted-early";
  return "indeterminate";
}
