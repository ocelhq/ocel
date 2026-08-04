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
    (byColo.get(colo) ?? byColo.set(colo, new Set()).get(colo)!).add(isolate);
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
  // The writer id carried by the cached sentinel, or null when the read missed.
  writer: string | null;
  hit: boolean;
  elapsedMs: number;
}

export type SentinelVerdict =
  // Another isolate read the sentinel: L1 as designed is viable.
  | "cross-isolate-visible"
  // Other isolates read and missed while the writer's own isolate hit: L1 is a
  // no-op and L2 must be sized for isolate-count fan-in.
  | "isolate-local"
  // Nothing ever hit, including the writer's isolate: the cache is inert here
  // (the workers.dev case), so the run measured nothing about isolates.
  | "never-cached"
  // No read ever landed on an isolate other than the writer's, so the run
  // cannot distinguish the two outcomes.
  | "inconclusive";

export interface SentinelSummary {
  verdict: SentinelVerdict;
  readerIsolates: number;
  crossIsolateHits: number;
  writerIsolateHits: number;
  misses: number;
  firstCrossIsolateHitMs: number | null;
  foreignColoObservations: number;
}

export function summarizeSentinel(
  writer: string,
  observations: SentinelObservation[],
): SentinelSummary {
  const writerColo = observations.find((o) => o.reader === writer)?.colo;
  const sameColo = writerColo
    ? observations.filter((o) => o.colo === writerColo)
    : observations.filter((o, _, all) => o.colo === all[0]?.colo);

  const cross = sameColo.filter((o) => o.reader !== writer);
  const crossHits = cross.filter((o) => o.hit);
  const writerHits = sameColo.filter((o) => o.reader === writer && o.hit);

  const summary = {
    readerIsolates: new Set(sameColo.map((o) => o.reader)).size,
    crossIsolateHits: crossHits.length,
    writerIsolateHits: writerHits.length,
    misses: sameColo.filter((o) => !o.hit).length,
    firstCrossIsolateHitMs: crossHits.length
      ? Math.min(...crossHits.map((o) => o.elapsedMs))
      : null,
    foreignColoObservations: observations.length - sameColo.length,
  };

  return { ...summary, verdict: sentinelVerdict(summary, cross.length) };
}

function sentinelVerdict(
  summary: Omit<SentinelSummary, "verdict">,
  crossReads: number,
): SentinelVerdict {
  if (summary.crossIsolateHits > 0) return "cross-isolate-visible";
  if (crossReads === 0) return "inconclusive";
  if (summary.writerIsolateHits > 0) return "isolate-local";
  return "never-cached";
}

export interface TtlObservation {
  hit: boolean;
  elapsedMs: number;
}

export type TtlVerdict =
  // A hit was observed at or past the requested TTL.
  | "honored"
  // The entry was already gone before the requested TTL elapsed.
  | "evicted-early"
  // The observed lifetime brackets the requested TTL, so the run proves neither.
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
}

export function summarizeTtl(
  requestedTtlSeconds: number,
  observations: TtlObservation[],
): TtlSummary {
  const polls = [...observations].sort((a, b) => a.elapsedMs - b.elapsedMs);
  const hits = polls.filter((p) => p.hit);
  const lastHitMs = hits.length ? hits[hits.length - 1]!.elapsedMs : null;
  const firstMissAfterLastHitMs =
    lastHitMs === null
      ? null
      : (polls.find((p) => !p.hit && p.elapsedMs > lastHitMs)?.elapsedMs ?? null);

  return {
    requestedTtlSeconds,
    lastHitMs,
    firstMissAfterLastHitMs,
    stillLiveAtEndOfWindow: lastHitMs !== null && firstMissAfterLastHitMs === null,
    transientMisses:
      lastHitMs === null
        ? 0
        : polls.filter((p) => !p.hit && p.elapsedMs < lastHitMs).length,
    polls: polls.length,
    verdict: ttlVerdict(requestedTtlSeconds * 1_000, lastHitMs, firstMissAfterLastHitMs),
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
