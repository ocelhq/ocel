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
  | "cross-isolate-visible"
  | "partially-visible"
  | "isolate-local"
  | "never-cached"
  | "inconclusive";

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
  | "honored"
  | "evicted-early"
  | "indeterminate"
  | "never-cached";

export interface TtlSummary {
  requestedTtlSeconds: number;
  verdict: TtlVerdict;
  lastHitMs: number | null;
  firstMissAfterLastHitMs: number | null;
  stillLiveAtEndOfWindow: boolean;
  transientMisses: number;
  polls: number;
  authoritativePolls: number;
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
