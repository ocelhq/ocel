import { describe, expect, it } from "vitest";

import {
  showsCrossIsolateVisibility,
  summarizeIsolates,
  summarizeSentinel,
  summarizeTtl,
  type SentinelObservation,
  type TtlObservation,
  type TtlRead,
} from "../src/analysis";

const observation = (
  partial: Partial<SentinelObservation> & Pick<SentinelObservation, "reader">,
): SentinelObservation => ({
  colo: "LHR",
  writer: null,
  hit: false,
  elapsedMs: 0,
  ...partial,
});

const times = <T>(count: number, make: (index: number) => T) =>
  Array.from({ length: count }, (_, index) => make(index));

describe("summarizeIsolates", () => {
  it("counts distinct isolates per colo", () => {
    const summary = summarizeIsolates([
      { colo: "LHR", isolate: "a" },
      { colo: "LHR", isolate: "a" },
      { colo: "LHR", isolate: "b" },
      { colo: "CDG", isolate: "c" },
    ]);

    expect(summary).toEqual([
      { colo: "CDG", isolates: 1, samples: 1 },
      { colo: "LHR", isolates: 2, samples: 3 },
    ]);
  });

  it("is empty for no samples", () => {
    expect(summarizeIsolates([])).toEqual([]);
  });
});

describe("summarizeSentinel", () => {
  it("reports cross-isolate visibility when nearly every foreign read hits", () => {
    const summary = summarizeSentinel("w", "LHR", [
      observation({ reader: "w", writer: "w", hit: true, elapsedMs: 20 }),
      ...times(10, (i) =>
        observation({ reader: `r${i}`, writer: "w", hit: true, elapsedMs: 90 + i }),
      ),
    ]);

    expect(summary.verdict).toBe("cross-isolate-visible");
    expect(summary.crossIsolateHits).toBe(10);
    expect(summary.crossIsolateReads).toBe(10);
    expect(summary.crossIsolateHitRate).toBe(1);
    expect(summary.readerIsolates).toBe(11);
    expect(summary.firstCrossIsolateHitMs).toBe(90);
  });

  it("reports partial visibility when only a fraction of foreign reads hit", () => {
    const summary = summarizeSentinel("w", "LHR", [
      observation({ reader: "w", writer: "w", hit: true, elapsedMs: 20 }),
      observation({ reader: "r1", writer: "w", hit: true, elapsedMs: 90 }),
      ...times(9, (i) => observation({ reader: `r${i + 2}`, hit: false, elapsedMs: 100 })),
    ]);

    expect(summary.verdict).toBe("partially-visible");
    expect(summary.crossIsolateHits).toBe(1);
    expect(summary.crossIsolateReads).toBe(10);
    expect(summary.crossIsolateHitRate).toBeCloseTo(0.1);
  });

  it("distinguishes a near-total hit rate from a sparse one", () => {
    const rate = (hits: number, misses: number) =>
      summarizeSentinel("w", "LHR", [
        ...times(hits, (i) => observation({ reader: `h${i}`, writer: "w", hit: true })),
        ...times(misses, (i) => observation({ reader: `m${i}`, hit: false })),
      ]).verdict;

    expect(rate(95, 5)).toBe("cross-isolate-visible");
    expect(rate(85, 15)).toBe("partially-visible");
  });

  it("reports isolate-local when other isolates read but only the writer hits", () => {
    const summary = summarizeSentinel("w", "LHR", [
      observation({ reader: "w", writer: "w", hit: true, elapsedMs: 15 }),
      observation({ reader: "r1", hit: false, elapsedMs: 30 }),
      observation({ reader: "r2", hit: false, elapsedMs: 45 }),
    ]);

    expect(summary.verdict).toBe("isolate-local");
    expect(summary.crossIsolateHits).toBe(0);
    expect(summary.crossIsolateHitRate).toBe(0);
    expect(summary.writerIsolateHits).toBe(1);
    expect(summary.firstCrossIsolateHitMs).toBeNull();
  });

  it("reports never-cached when not even the writer's isolate hits", () => {
    const summary = summarizeSentinel("w", "LHR", [
      observation({ reader: "w", hit: false, elapsedMs: 10 }),
      observation({ reader: "r1", hit: false, elapsedMs: 20 }),
    ]);

    expect(summary.verdict).toBe("never-cached");
  });

  it("is inconclusive when every read landed on the writer's own isolate", () => {
    const summary = summarizeSentinel("w", "LHR", [
      observation({ reader: "w", writer: "w", hit: true, elapsedMs: 10 }),
      observation({ reader: "w", writer: "w", hit: true, elapsedMs: 20 }),
    ]);

    expect(summary.verdict).toBe("inconclusive");
    expect(summary.readerIsolates).toBe(1);
    expect(summary.crossIsolateHitRate).toBeNull();
  });

  it("is inconclusive with no observations at all", () => {
    expect(summarizeSentinel("w", "LHR", []).verdict).toBe("inconclusive");
  });

  it("counts only observations from the writer's colo", () => {
    const summary = summarizeSentinel("w", "LHR", [
      observation({ reader: "r1", writer: "w", hit: true, elapsedMs: 50 }),
      observation({ reader: "r2", colo: "CDG", hit: false, elapsedMs: 60 }),
    ]);

    expect(summary.foreignColoObservations).toBe(1);
    expect(summary.readerIsolates).toBe(1);
    expect(summary.verdict).toBe("cross-isolate-visible");
  });

  it("uses the writer's reported colo even when the writer never served a read", () => {
    const observations = [
      observation({ reader: "r1", colo: "CDG", hit: false, elapsedMs: 10 }),
      observation({ reader: "r2", colo: "LHR", writer: "w", hit: true, elapsedMs: 20 }),
    ];

    expect(summarizeSentinel("w", "LHR", observations)).toMatchObject({
      verdict: "cross-isolate-visible",
      crossIsolateHits: 1,
      crossIsolateReads: 1,
      foreignColoObservations: 1,
    });
  });

  it("keeps every observation when the writer reported no colo", () => {
    const summary = summarizeSentinel("w", null, [
      observation({ reader: "r1", colo: "unknown", writer: "w", hit: true }),
    ]);

    expect(summary.foreignColoObservations).toBe(0);
    expect(summary.verdict).toBe("cross-isolate-visible");
  });
});

describe("showsCrossIsolateVisibility", () => {
  it("is true for both sharing verdicts and false otherwise", () => {
    expect(showsCrossIsolateVisibility("cross-isolate-visible")).toBe(true);
    expect(showsCrossIsolateVisibility("partially-visible")).toBe(true);
    expect(showsCrossIsolateVisibility("isolate-local")).toBe(false);
    expect(showsCrossIsolateVisibility("never-cached")).toBe(false);
    expect(showsCrossIsolateVisibility("inconclusive")).toBe(false);
  });
});

describe("summarizeTtl", () => {
  const read = (partial: Partial<TtlRead> = {}): TtlRead => ({
    isolate: "w",
    hit: true,
    ageSeconds: null,
    cacheControl: null,
    ...partial,
  });
  const poll = (elapsedMs: number, hit: boolean, reads?: TtlRead[]): TtlObservation => ({
    elapsedMs,
    reads: reads ?? [read({ hit })],
  });
  const summarize = (
    requested: number,
    observations: TtlObservation[],
    crossIsolateVisible = true,
  ) => summarizeTtl(requested, "w", observations, crossIsolateVisible);

  it("calls the TTL honored only once a hit is observed at or past it", () => {
    const summary = summarize(10, [
      poll(9_000, true),
      poll(10_500, true),
      poll(11_500, false),
    ]);

    expect(summary.lastHitMs).toBe(10_500);
    expect(summary.firstMissAfterLastHitMs).toBe(11_500);
    expect(summary.verdict).toBe("honored");
  });

  it("declines to judge when the observed lifetime straddles the requested TTL", () => {
    const summary = summarize(10, [poll(1_000, true), poll(6_000, true), poll(11_000, false)]);

    expect(summary.lastHitMs).toBe(6_000);
    expect(summary.verdict).toBe("indeterminate");
  });

  it("calls a lifetime shorter than requested evicted-early", () => {
    const summary = summarize(10, [poll(1_000, true), poll(2_000, false), poll(3_000, false)]);

    expect(summary.lastHitMs).toBe(1_000);
    expect(summary.firstMissAfterLastHitMs).toBe(2_000);
    expect(summary.verdict).toBe("evicted-early");
  });

  it("ignores a transient miss followed by a later hit", () => {
    const summary = summarize(10, [
      poll(1_000, true),
      poll(2_000, false),
      poll(3_000, true),
      poll(12_000, false),
    ]);

    expect(summary.lastHitMs).toBe(3_000);
    expect(summary.transientMisses).toBe(1);
  });

  it("flags an entry that was still live when polling stopped", () => {
    const summary = summarize(10, [poll(1_000, true), poll(20_000, true)]);

    expect(summary.firstMissAfterLastHitMs).toBeNull();
    expect(summary.stillLiveAtEndOfWindow).toBe(true);
    expect(summary.verdict).toBe("honored");
  });

  it("stays indeterminate when polling stopped before the requested TTL", () => {
    const summary = summarize(10, [poll(1_000, true), poll(4_000, true)]);

    expect(summary.stillLiveAtEndOfWindow).toBe(true);
    expect(summary.verdict).toBe("indeterminate");
  });

  it("reports never-cached when nothing ever hit", () => {
    expect(summarize(10, [poll(500, false)]).verdict).toBe("never-cached");
    expect(summarize(10, []).verdict).toBe("indeterminate");
  });

  it("counts only polls that reached the writer's isolate when the cache is not shared", () => {
    const summary = summarize(
      10,
      [
        poll(1_000, false, [read({ isolate: "w", hit: true })]),
        poll(2_000, false, [read({ isolate: "other", hit: false })]),
        poll(3_000, false, [read({ isolate: "other", hit: false })]),
        poll(12_000, false, [read({ isolate: "w", hit: true })]),
      ],
      false,
    );

    expect(summary.polls).toBe(4);
    expect(summary.authoritativePolls).toBe(2);
    expect(summary.lastHitMs).toBe(12_000);
    expect(summary.verdict).toBe("honored");
  });

  it("is indeterminate when no poll ever reached the writer's isolate", () => {
    const summary = summarize(
      10,
      [
        poll(1_000, false, [read({ isolate: "other", hit: false })]),
        poll(2_000, false, [read({ isolate: "other", hit: false })]),
      ],
      false,
    );

    expect(summary.authoritativePolls).toBe(0);
    expect(summary.verdict).toBe("indeterminate");
  });

  it("would have called an isolate-local run evicted-early without the filter", () => {
    const observations = [
      poll(1_000, false, [read({ isolate: "w", hit: true })]),
      poll(2_000, false, [read({ isolate: "other", hit: false })]),
      poll(3_000, false, [read({ isolate: "other", hit: false })]),
    ];

    expect(summarize(10, observations, true).verdict).toBe("evicted-early");
    expect(summarize(10, observations, false).verdict).toBe("indeterminate");
  });

  it("collects Cloudflare's own age and cache-control from every hit", () => {
    const summary = summarize(10, [
      poll(1_000, true, [read({ ageSeconds: 1, cacheControl: "max-age=10" })]),
      poll(9_000, true, [
        read({ ageSeconds: 9, cacheControl: "max-age=10" }),
        read({ isolate: "other", hit: false }),
      ]),
    ]);

    expect(summary.maxObservedAgeSeconds).toBe(9);
    expect(summary.observedCacheControl).toEqual(["max-age=10"]);
  });
});
