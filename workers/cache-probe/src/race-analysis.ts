// Aggregation over the race. As in src/analysis.ts, every elapsed time here was
// measured by the driver against its own clock; no two Worker clocks are ever
// differenced. The gap sweep goes further and never differences ANY timestamps
// across hosts: the delay between two racers is imposed by the driver and both
// racers pay the same round trip, so the driver's latency cancels out of it
// rather than being subtracted from it.

export interface RaceOutcome {
  seq: number;
  claimed: boolean;
  isolate: string;
  colo: string;
}

export interface Distribution {
  count: number;
  min: number;
  p10: number;
  median: number;
  p90: number;
  max: number;
  mean: number;
}

// Nearest-rank, on the sample itself: no interpolation invents a value the run
// never observed, and no outlier is dropped.
export function distribution(values: number[]): Distribution | null {
  if (!values.length) return null;
  const sorted = [...values].sort((a, b) => a - b);
  const at = (q: number) => sorted[Math.min(sorted.length - 1, Math.ceil(q * sorted.length) - 1)]!;
  return {
    count: sorted.length,
    min: sorted[0]!,
    p10: at(0.1),
    median: at(0.5),
    p90: at(0.9),
    max: sorted[sorted.length - 1]!,
    mean: sorted.reduce((sum, v) => sum + v, 0) / sorted.length,
  };
}

// ---------------------------------------------------------------------------
// Phase 1 — the gap sweep. Two racers, the second fired a driver-imposed Δ after
// the first was SENT. The statistic is the probability the second claims given
// it landed on a different isolate in the same colo and the first did claim.

export interface GapTrial {
  deltaMs: number;
  // What the driver actually achieved between the two sends. Reported, never
  // used to correct the statistic: a trial is filed under the Δ it asked for.
  achievedDeltaMs: number;
  a: RaceOutcome;
  b: RaceOutcome;
}

export type GapExclusion =
  // Neither racer claimed a key nobody had ever written. The instrument is
  // wrong, not the cache — P4.
  | "zero-claims"
  // The leader did not claim but the follower did: the two arrived out of the
  // order the driver imposed, so the trial measures nothing about Δ.
  | "leader-did-not-claim"
  // The racers hit different colos, so they never shared a cache.
  | "mixed-colo"
  // The racers landed on one isolate. Production collapses those in L0 before
  // the sentinel is ever consulted, so such a trial measures L0, not L1.
  | "same-isolate";

export type GapClassification = GapExclusion | "decidable";

export function classifyGapTrial(trial: GapTrial): GapClassification {
  const { a, b } = trial;
  if (!a.claimed && !b.claimed) return "zero-claims";
  if (!a.claimed) return "leader-did-not-claim";
  if (a.colo !== b.colo) return "mixed-colo";
  if (a.isolate === b.isolate) return "same-isolate";
  return "decidable";
}

export interface GapSummary {
  deltaMs: number;
  trials: number;
  decidable: number;
  secondClaims: number;
  // Null when nothing was decidable: a rate over zero trials is not zero.
  secondClaimRate: number | null;
  excluded: Record<GapExclusion, number>;
  achievedDeltaMs: Distribution | null;
}

const noExclusions = (): Record<GapExclusion, number> => ({
  "zero-claims": 0,
  "leader-did-not-claim": 0,
  "mixed-colo": 0,
  "same-isolate": 0,
});

export function summarizeGap(deltaMs: number, trials: GapTrial[]): GapSummary {
  const excluded = noExclusions();
  const decidable: GapTrial[] = [];
  for (const trial of trials) {
    const verdict = classifyGapTrial(trial);
    if (verdict === "decidable") decidable.push(trial);
    else excluded[verdict] += 1;
  }
  const secondClaims = decidable.filter((t) => t.b.claimed).length;
  return {
    deltaMs,
    trials: trials.length,
    decidable: decidable.length,
    secondClaims,
    secondClaimRate: decidable.length ? secondClaims / decidable.length : null,
    excluded,
    achievedDeltaMs: distribution(trials.map((t) => t.achievedDeltaMs)),
  };
}

export type WindowVerdict =
  // A Δ at which the follower stops claiming, confirmed by the next Δ up.
  | "measured"
  // The smallest Δ swept already suppresses the follower, so the window is at
  // or below it — including Δ=0, which means the claim is colo-visible at once.
  | "below-resolution"
  // The follower was still claiming at the widest Δ swept, or the drop did not
  // hold at the next step. The window is larger than this sweep can see.
  | "exceeds-sweep"
  // Too few decidable trials somewhere in the sweep to order the Δs at all.
  | "inconclusive";

export interface WindowResult {
  verdict: WindowVerdict;
  // The window in milliseconds, when one was found. An upper bound under
  // `below-resolution`; exact to the sweep's resolution under `measured`.
  windowMs: number | null;
  threshold: number;
  minDecidable: number;
  // The Δs the sweep could not decide, which is why a verdict is inconclusive.
  undecidedDeltaMs: number[];
}

// The follower's claim probability at or below this reads as suppressed.
const SUPPRESSED_RATE = 0.05;
// Below this many decidable trials at a Δ, the rate at that Δ is noise.
const MIN_DECIDABLE = 20;

export function chooseWindow(
  summaries: GapSummary[],
  options: { threshold?: number; minDecidable?: number } = {},
): WindowResult {
  const threshold = options.threshold ?? SUPPRESSED_RATE;
  const minDecidable = options.minDecidable ?? MIN_DECIDABLE;
  const sorted = [...summaries].sort((a, b) => a.deltaMs - b.deltaMs);
  const undecidedDeltaMs = sorted
    .filter((s) => s.decidable < minDecidable)
    .map((s) => s.deltaMs);

  const base = { threshold, minDecidable, undecidedDeltaMs };
  // A hole anywhere in the sweep means the Δs cannot be ordered against each
  // other, and the smallest suppressing Δ is exactly an ordering claim.
  if (!sorted.length || undecidedDeltaMs.length) {
    return { ...base, verdict: "inconclusive", windowMs: null };
  }

  const suppressed = (s: GapSummary) => s.secondClaimRate! < threshold;
  const index = sorted.findIndex(
    (s, i) => i + 1 < sorted.length && suppressed(s) && suppressed(sorted[i + 1]!),
  );
  if (index === -1) return { ...base, verdict: "exceeds-sweep", windowMs: null };
  return {
    ...base,
    verdict: index === 0 ? "below-resolution" : "measured",
    windowMs: sorted[index]!.deltaMs,
  };
}

// ---------------------------------------------------------------------------
// Phase 2 — the burst. N racers at one cold key, as simultaneously as the driver
// can manage, yielding escapes per colo per stale event directly.

export interface BurstTrial {
  size: number;
  // max − min of the driver's own pre-send timestamps. If this exceeds the
  // window, the racers were not concurrent relative to what is being measured
  // and the escape count is biased downward.
  dispersionMs: number;
  outcomes: RaceOutcome[];
}

export interface TrialEscapes {
  rawClaims: number;
  // The production-relevant number. Production runs refreshOnce (L0) inside
  // admitRefresh, so two concurrent requests on one isolate collapse to one
  // claim before the sentinel is consulted. The probe has no L0, so the raw
  // count double-counts an isolate and only the collapsed one is comparable.
  escapes: number;
  distinctIsolates: number;
  distinctColos: number;
}

export function escapesOf(outcomes: RaceOutcome[]): TrialEscapes {
  const claimed = outcomes.filter((o) => o.claimed);
  return {
    rawClaims: claimed.length,
    escapes: new Set(claimed.map((o) => o.isolate)).size,
    distinctIsolates: new Set(outcomes.map((o) => o.isolate)).size,
    distinctColos: new Set(outcomes.map((o) => o.colo)).size,
  };
}

export type BurstClassification =
  // No racer claimed a key nobody had written — the instrument is broken (P4).
  | "zero-claims"
  // The burst spanned colos, so it says nothing about one colo's cache.
  | "mixed-colo"
  // Every racer landed on one isolate: L0's territory, not L1's. Kept in its
  // own bucket rather than discarded, because escapes=1 here would otherwise
  // read as perfect L1 suppression.
  | "single-isolate"
  | "counted";

export function classifyBurstTrial(trial: BurstTrial): BurstClassification {
  const { rawClaims, distinctColos, distinctIsolates } = escapesOf(trial.outcomes);
  if (rawClaims === 0) return "zero-claims";
  if (distinctColos !== 1) return "mixed-colo";
  if (distinctIsolates === 1) return "single-isolate";
  return "counted";
}

export interface BurstSummary {
  size: number;
  trials: number;
  counted: number;
  singleIsolateTrials: number;
  // Above 0.1 the run is reported as having measured L0 more than it measured
  // L1, whatever the headline says.
  singleIsolateRate: number | null;
  discarded: { "zero-claims": number; "mixed-colo": number };
  escapes: Distribution | null;
  rawClaims: Distribution | null;
  distinctIsolates: Distribution | null;
  dispersionMs: Distribution | null;
  // True when escapes is a LOWER bound: the racers' arrival spread was not
  // small against the window, so later racers could already see the claim.
  // Also true when the window is unknown, because then it cannot be ruled out.
  lowerBound: boolean;
  // Trials whose responses do not account for their racers one-for-one. Any
  // violation means the instrument is double-counting — the zone serving one
  // racer's body to another is exactly how that would happen — and the run is
  // void, so this aborts rather than being footnoted.
  invariantViolations: number;
}

// Every racer must be answered exactly once, by its own request, and no isolate
// may escape more times than it appeared.
export function accountsForItsRacers(trial: BurstTrial): boolean {
  const { escapes, distinctIsolates } = escapesOf(trial.outcomes);
  return (
    trial.outcomes.length === trial.size &&
    new Set(trial.outcomes.map((o) => o.seq)).size === trial.size &&
    escapes <= distinctIsolates
  );
}

export function summarizeBurst(
  size: number,
  trials: BurstTrial[],
  windowMs: number | null,
): BurstSummary {
  const discarded = { "zero-claims": 0, "mixed-colo": 0 };
  let singleIsolateTrials = 0;
  const counted: { trial: BurstTrial; escapes: TrialEscapes }[] = [];
  for (const trial of trials) {
    const verdict = classifyBurstTrial(trial);
    if (verdict === "counted") counted.push({ trial, escapes: escapesOf(trial.outcomes) });
    else if (verdict === "single-isolate") singleIsolateTrials += 1;
    else discarded[verdict] += 1;
  }

  const dispersion = distribution(counted.map(({ trial }) => trial.dispersionMs));
  return {
    size,
    trials: trials.length,
    counted: counted.length,
    singleIsolateTrials,
    singleIsolateRate: trials.length ? singleIsolateTrials / trials.length : null,
    discarded,
    escapes: distribution(counted.map(({ escapes }) => escapes.escapes)),
    rawClaims: distribution(counted.map(({ escapes }) => escapes.rawClaims)),
    distinctIsolates: distribution(counted.map(({ escapes }) => escapes.distinctIsolates)),
    dispersionMs: dispersion,
    lowerBound:
      windowMs === null || (dispersion !== null && dispersion.median > windowMs),
    invariantViolations: trials.filter((trial) => !accountsForItsRacers(trial)).length,
  };
}

// ---------------------------------------------------------------------------
// The sizing PR 8 cites. λ is NOT measured here and is not measurable here: it
// is the arrival rate on one hot route in one colo, a property of the operator's
// traffic rather than of Cloudflare. It is a parameter, and the table exists so
// that PR 8 reads a row rather than hard-coding a guess.

export interface SizingInputs {
  windowSeconds: number;
  // A lower bound, always. No isolate escapes twice, so it caps E however high
  // λ goes — but the real isolate count is only ever bounded from below.
  isolatesPerColo: number;
  colos: number;
  sentinelTtlSeconds: number;
}

export interface SizingRow {
  lambdaPerColo: number;
  escapesPerColo: number;
  fanInPerStaleEvent: number;
  sustainedRequestsPerSecond: number;
  cappedByIsolates: boolean;
}

export function sizingTable(inputs: SizingInputs, lambdas: number[]): SizingRow[] {
  return lambdas.map((lambdaPerColo) => {
    const uncapped = 1 + lambdaPerColo * inputs.windowSeconds;
    const escapesPerColo = Math.min(uncapped, inputs.isolatesPerColo);
    const fanInPerStaleEvent = inputs.colos * escapesPerColo;
    return {
      lambdaPerColo,
      escapesPerColo,
      fanInPerStaleEvent,
      sustainedRequestsPerSecond: fanInPerStaleEvent / inputs.sentinelTtlSeconds,
      cappedByIsolates: uncapped > inputs.isolatesPerColo,
    };
  });
}
