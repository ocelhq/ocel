// Aggregation over the race. As in src/analysis.ts, every elapsed time here was
// measured by the driver against its own clock; no two Worker clocks are ever
// differenced. The gap sweep goes further and never differences ANY timestamps
// across hosts: the delay between two racers is imposed by the driver and both
// racers pay the same round trip, so the driver's latency cancels out of it
// rather than being subtracted from it.

import { Abort } from "./abort.ts";

export interface RaceOutcome {
  seq: number;
  claimed: boolean;
  isolate: string;
  colo: string;
  // The admission delay this racer's worker drew and slept before claiming, on
  // the worker's side of the network. Zero when the trial asked for no jitter.
  delayMs: number;
}

// What the worker answers a racer with. Narrowed into a RaceOutcome by
// `outcomeOf`, which is where every check on the response's identity lives.
export interface RaceResponse {
  isolate: string;
  colo: string | null;
  host: string;
  claimed: boolean;
  key: string;
  scope: string;
  seq: string | null;
  delayMs: number;
}

// What the driver observed of one racer's whole request, from the call that
// queued it to the body being read. It is an OVER-estimate of the time the
// worker spent, which is what makes it usable as a ceiling on the delay the
// worker claims to have slept.
export interface RacerTiming {
  jitterMs: number;
  elapsedMs: number;
}

// The one live duplicate detector in this instrument. The zone's edge cache
// sits in front of the worker, so a racer being answered with ANOTHER racer's
// body is the way a duplicate claim gets manufactured; the worker echoes the
// key and seq it was asked for, and the echo is checked here against what was
// sent. A null colo fails the same way rather than defaulting: two racers whose
// colo is unknown would compare equal and pass the mixed-colo gate that exists
// to catch exactly that.
export function outcomeOf(
  response: RaceResponse,
  key: string,
  seq: number,
  timing: RacerTiming,
): RaceOutcome {
  if (response.key !== key || response.seq !== String(seq)) {
    throw new Abort(
      `racer ${seq} on key ${key} was answered for key ${response.key} seq ` +
        `${response.seq}. One racer received another's body; the run is void.`,
    );
  }
  if (response.colo === null) {
    throw new Abort(
      `racer ${seq} on key ${key} was served without a colo. request.cf was absent, so ` +
        "no trial can be attributed to a colo and the run is void.",
    );
  }
  // Two live checks on the jitter, both of which fail the run rather than
  // footnoting it. A worker that ignored --jitter, or drew from a different
  // window than the one asked for, breaks the first; a worker that reported a
  // delay it never slept breaks the second, because the driver's elapsed time
  // starts before the request is even queued and so bounds the worker's own
  // wall clock from above.
  if (
    !Number.isFinite(response.delayMs) ||
    response.delayMs < 0 ||
    response.delayMs > timing.jitterMs
  ) {
    throw new Abort(
      `racer ${seq} on key ${key} reported an admission delay of ${response.delayMs}ms, ` +
        `outside [0, ${timing.jitterMs}]. The worker is not drawing the window the run ` +
        "asked for; the run is void.",
    );
  }
  if (response.delayMs > timing.elapsedMs) {
    throw new Abort(
      `racer ${seq} on key ${key} reported sleeping ${response.delayMs}ms inside a request ` +
        `the driver measured at ${timing.elapsedMs.toFixed(1)}ms. The delay was reported ` +
        "but not spent; the run is void.",
    );
  }

  return {
    seq,
    claimed: response.claimed,
    isolate: response.isolate,
    colo: response.colo,
    delayMs: response.delayMs,
  };
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
  // Neither racer claimed a key nobody had ever written. STRUCTURALLY
  // UNREACHABLE, like its burst twin: whichever racer runs `match` first on a
  // fresh UUID key must miss and therefore claim. It asserts that reasoning; a
  // zero here is not a measurement and must not be reported as one.
  | "zero-claims"
  // The leader did not claim but the follower did: the two arrived out of the
  // order the driver imposed, so the trial measures nothing about Δ.
  | "leader-did-not-claim"
  // The racers hit different colos, so they never shared a cache.
  | "mixed-colo"
  // The racers landed on one isolate. Production collapses those in L0 before
  // the sentinel is ever consulted, so such a trial measures L0, not L1.
  | "same-isolate";

type GapClassification = GapExclusion | "decidable";

function classifyGapTrial(trial: GapTrial): GapClassification {
  const { a, b } = trial;
  if (!a.claimed && !b.claimed) return "zero-claims";
  if (!a.claimed) return "leader-did-not-claim";
  if (a.colo !== b.colo) return "mixed-colo";
  if (a.isolate === b.isolate) return "same-isolate";
  return "decidable";
}

export interface GapSummary {
  deltaMs: number;
  // Trials the runner tried, against the trials that produced a result. See
  // BurstSummary.attempted.
  attempted: number;
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

export function summarizeGap(
  deltaMs: number,
  trials: GapTrial[],
  attempted = trials.length,
): GapSummary {
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
    attempted,
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
  // The admission-jitter window every racer in this trial drew from. Zero
  // reproduces the un-jittered burst ocelhq-wvag.9 measured.
  jitterMs: number;
  // max − min over the moments undici wrote each racer's first byte to its
  // socket, NOT over the driver's loop. The loop's own spread is microseconds
  // and measures nothing: a racer's request is still unsent when the next
  // iteration begins. If this exceeds the window, the racers were not
  // concurrent relative to what is being measured and the escape count is
  // biased downward.
  dispersionMs: number;
  outcomes: RaceOutcome[];
}

interface TrialEscapes {
  rawClaims: number;
  // The production-relevant number. Production runs refreshOnce (L0) inside
  // admitRefresh, so two concurrent requests on one isolate collapse to one
  // claim before the sentinel is consulted. The probe has no L0, so the raw
  // count double-counts an isolate and only the collapsed one is comparable.
  escapes: number;
  // Escaping isolates whose draw put them more than a window AFTER the trial's
  // first claim. Under jitter these are the escapes the window cannot explain:
  // by the time they ran `match`, the first claim had had longer than W to
  // become visible and did not suppress them. If E stops falling as J grows,
  // this is the term that says whether the floor is mutually blind isolates
  // (lateEscapes ~= escapes - 1) or merely a window not yet outrun
  // (lateEscapes ~= 0). Null when no window was supplied, because then no claim
  // can be called late.
  lateEscapes: number | null;
  distinctIsolates: number;
  distinctColos: number;
}

function escapesOf(outcomes: RaceOutcome[], windowMs: number | null): TrialEscapes {
  const claimed = outcomes.filter((o) => o.claimed);
  // An isolate that claimed twice claimed once as far as production is
  // concerned, and the earlier draw is the one that took the admission.
  const firstDrawByIsolate = new Map<string, number>();
  for (const outcome of claimed) {
    const seen = firstDrawByIsolate.get(outcome.isolate);
    if (seen === undefined || outcome.delayMs < seen) {
      firstDrawByIsolate.set(outcome.isolate, outcome.delayMs);
    }
  }
  const draws = [...firstDrawByIsolate.values()].sort((a, b) => a - b);
  // Measured against the FIRST claim rather than the nearest preceding one:
  // the first has had the longest to propagate, so it is the most generous
  // explanation available, and a claim it cannot explain is unexplained.
  const earliest = draws[0];
  return {
    rawClaims: claimed.length,
    escapes: firstDrawByIsolate.size,
    lateEscapes:
      windowMs === null || earliest === undefined
        ? null
        : draws.filter((draw) => draw > earliest + windowMs).length,
    distinctIsolates: new Set(outcomes.map((o) => o.isolate)).size,
    distinctColos: new Set(outcomes.map((o) => o.colo)).size,
  };
}

type BurstClassification =
  // No racer claimed a key nobody had written. STRUCTURALLY UNREACHABLE with
  // this claim primitive: the globally first `match` on a fresh UUID key must
  // miss, so somebody claims. Kept as an assertion of that reasoning, not as
  // evidence — a zero here is a theorem, and the doc must not cite it as a
  // measurement.
  | "zero-claims"
  // The burst spanned colos, so it says nothing about one colo's cache. Also
  // unreachable from a single driver host, which reaches exactly one colo; it
  // exists so a two-host run would not silently pool two caches.
  | "mixed-colo"
  // Every racer landed on one isolate: L0's territory, not L1's. Kept in its
  // own bucket rather than discarded, because escapes=1 here would otherwise
  // read as perfect L1 suppression.
  | "single-isolate"
  | "counted";

function classifyBurstTrial({
  rawClaims,
  distinctColos,
  distinctIsolates,
}: TrialEscapes): BurstClassification {
  if (rawClaims === 0) return "zero-claims";
  if (distinctColos !== 1) return "mixed-colo";
  if (distinctIsolates === 1) return "single-isolate";
  return "counted";
}

export interface BurstSummary {
  size: number;
  jitterMs: number;
  // Trials the runner tried, against the trials that produced a result. They
  // differ by the transport failures the runner discarded, and a summary that
  // reported only the latter would shrink its own denominator in silence.
  attempted: number;
  trials: number;
  counted: number;
  singleIsolateTrials: number;
  // Above 0.1 the run is reported as having measured L0 more than it measured
  // L1, whatever the headline says.
  singleIsolateRate: number | null;
  discarded: { "zero-claims": number; "mixed-colo": number };
  escapes: Distribution | null;
  // Per trial, the escapes the window cannot account for. Null when the run had
  // no window to judge lateness against.
  lateEscapes: Distribution | null;
  rawClaims: Distribution | null;
  distinctIsolates: Distribution | null;
  dispersionMs: Distribution | null;
  // Every racer's drawn delay across the counted trials, which is what
  // jitterVerdict is read off.
  delayMs: Distribution | null;
  // True when escapes is a LOWER bound: the racers' arrival spread was not
  // small against the window, so later racers could already see the claim.
  // Also true when the window is unknown, because then it cannot be ruled out.
  lowerBound: boolean;
}

export function summarizeBurst(
  size: number,
  trials: BurstTrial[],
  windowMs: number | null,
  attempted = trials.length,
  jitterMs = 0,
): BurstSummary {
  const discarded = { "zero-claims": 0, "mixed-colo": 0 };
  let singleIsolateTrials = 0;
  const counted: { trial: BurstTrial; escapes: TrialEscapes }[] = [];
  for (const trial of trials) {
    const escapes = escapesOf(trial.outcomes, windowMs);
    const verdict = classifyBurstTrial(escapes);
    if (verdict === "counted") counted.push({ trial, escapes });
    else if (verdict === "single-isolate") singleIsolateTrials += 1;
    else discarded[verdict] += 1;
  }

  const dispersion = distribution(counted.map(({ trial }) => trial.dispersionMs));
  const late = counted
    .map(({ escapes }) => escapes.lateEscapes)
    .filter((value): value is number => value !== null);
  return {
    size,
    jitterMs,
    attempted,
    trials: trials.length,
    counted: counted.length,
    singleIsolateTrials,
    singleIsolateRate: trials.length ? singleIsolateTrials / trials.length : null,
    discarded,
    escapes: distribution(counted.map(({ escapes }) => escapes.escapes)),
    lateEscapes: distribution(late),
    rawClaims: distribution(counted.map(({ escapes }) => escapes.rawClaims)),
    distinctIsolates: distribution(counted.map(({ escapes }) => escapes.distinctIsolates)),
    dispersionMs: dispersion,
    delayMs: distribution(counted.flatMap(({ trial }) => trial.outcomes.map((o) => o.delayMs))),
    // Send dispersion still biases escapes downward when it approaches the
    // window, jitter or no jitter: a racer that arrives late enough sees the
    // claim for reasons that have nothing to do with its draw.
    //
    // Read off the P90, not the median. Dispersion's tail is where this breaks:
    // at J = 2000ms the median was 1.51ms and the p90 was 115ms — fourteen times
    // W in at least a tenth of the trials — and a median-only guard printed
    // those rows clean. A guard whose statistic cannot see its own failure is
    // the defect this instrument keeps producing; the median stays reported
    // beside it as the secondary signal.
    lowerBound: windowMs === null || (dispersion !== null && dispersion.p90 > windowMs),
  };
}

export type JitterVerdict =
  // The run asked for no jitter, and none was drawn. The un-jittered baseline.
  | "none-requested"
  // Draws land on both sides of the window's midpoint, so the worker is drawing
  // the window the run asked for.
  | "spread"
  // Jitter was asked for and the draws do not cover the window: a worker that
  // ignored the parameter, or a constant standing in for a draw. Over hundreds
  // of racers this cannot happen by chance, so it is a wiring fault.
  | "degenerate";

// Read off the drawn delays rather than off the request that asked for them,
// because the parameter being accepted is not evidence that it was used.
export function jitterVerdict(jitterMs: number, delayMs: Distribution | null): JitterVerdict {
  // An un-jittered run has nothing left to check here: outcomeOf already aborts
  // any racer reporting a delay outside [0, jitterMs], so a worker that jittered
  // when none was asked for has voided the run long before this reads a
  // distribution it could only ever find at zero.
  if (jitterMs === 0) return "none-requested";
  if (delayMs === null) return "degenerate";
  const midpoint = jitterMs / 2;
  return delayMs.max >= midpoint && delayMs.min <= midpoint ? "spread" : "degenerate";
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
