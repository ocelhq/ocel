import diagnosticsChannel from "node:diagnostics_channel";
import { randomUUID } from "node:crypto";
import { mkdir, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { Client } from "undici";

import { Abort } from "../src/abort.ts";
import { sleep } from "../src/race.ts";
import { parseRaceOptions, type RaceOptions } from "../src/race-options.ts";
import {
  chooseWindow,
  distribution,
  jitterVerdict,
  outcomeOf,
  sizingTable,
  summarizeBurst,
  summarizeGap,
  type BurstSummary,
  type BurstTrial,
  type Distribution,
  type GapSummary,
  type GapTrial,
  type RaceResponse,
  type SizingRow,
  type WindowResult,
} from "../src/race-analysis.ts";

const openClient = (origin: string) => new Client(origin, { pipelining: 1 });

async function call<T>(client: Client, method: "GET" | "POST", path: string): Promise<T> {
  const response = await client.request({ path, method });
  const body = await response.body.text();
  if (response.statusCode !== 200) {
    throw new Error(`${method} ${path} -> ${response.statusCode}: ${body.slice(0, 120)}`);
  }
  return JSON.parse(body) as T;
}

const sentAtByPath = new Map<string, number>();
diagnosticsChannel.subscribe("undici:client:sendHeaders", (message) => {
  const { request } = message as { request: { path: string } };
  sentAtByPath.set(request.path, performance.now());
});

function dispersionOf(paths: string[]): number {
  const sent = paths.map((path) => sentAtByPath.get(path));
  if (sent.some((at) => at === undefined)) {
    throw new Abort(
      "undici did not report a send for every racer, so the burst's concurrency cannot be " +
        "bounded. Escapes without that bound are not interpretable; the run is void.",
    );
  }
  return Math.max(...(sent as number[])) - Math.min(...(sent as number[]));
}

async function raceCall(client: Client, path: string) {
  const started = performance.now();
  const response = await call<RaceResponse>(client, "POST", path);
  return { response, elapsedMs: performance.now() - started };
}

interface Identity {
  isolate: string;
  colo: string | null;
  host: string;
}

async function openRacers(origin: string, count: number): Promise<Client[]> {
  const clients = Array.from({ length: count }, () => openClient(origin));
  await Promise.all(clients.map((client) => call<Identity>(client, "GET", "/identity")));
  return clients;
}

const closeRacers = (clients: Client[]) => Promise.all(clients.map((c) => c.close()));

interface ControlArm {
  scope: string;
  writer: string;
  writerColo: string | null;
  verified: boolean;
  reads: number;
  hits: number;
  crossIsolateReads: number;
  crossIsolateHits: number;
  readerIsolates: number;
  visible: boolean;
}

async function controlArm(
  origin: string,
  run: string,
  scope: string,
  fanout: number,
): Promise<ControlArm> {
  const writerClient = openClient(origin);
  const written = await call<Identity & { verified: boolean }>(
    writerClient,
    "GET",
    `/control?run=${run}&scope=${scope}&mode=write`,
  );
  await writerClient.close();

  const readers = await openRacers(origin, fanout);
  const reads = await Promise.all(
    readers.map((client, seq) =>
      call<Identity & { hit: boolean; writer: string | null }>(
        client,
        "GET",
        `/control?run=${run}&scope=${scope}&mode=read&seq=${seq}`,
      ),
    ),
  );
  await closeRacers(readers);

  const sameColo = reads.filter((r) => r.colo === written.colo);
  const cross = sameColo.filter((r) => r.isolate !== written.isolate);
  const crossHits = cross.filter((r) => r.hit);
  return {
    scope,
    writer: written.isolate,
    writerColo: written.colo,
    verified: written.verified,
    reads: reads.length,
    hits: reads.filter((r) => r.hit).length,
    crossIsolateReads: cross.length,
    crossIsolateHits: crossHits.length,
    readerIsolates: new Set(sameColo.map((r) => r.isolate)).size,
    visible: written.verified && crossHits.length > 0,
  };
}

type ControlVerdict =
  | "both-scopes-visible"
  | "offzone-inert"
  | "onzone-inert"
  | "cache-inert"
  | "inconclusive";

function controlVerdict(onzone: ControlArm, offzone: ControlArm): ControlVerdict {
  if (!onzone.verified && !offzone.verified) return "cache-inert";
  if (onzone.crossIsolateReads === 0 || offzone.crossIsolateReads === 0) {
    return "inconclusive";
  }
  if (onzone.visible && offzone.visible) return "both-scopes-visible";
  if (onzone.visible) return "offzone-inert";
  if (offzone.visible) return "onzone-inert";
  return "cache-inert";
}

async function control(options: RaceOptions, origin: string) {
  const run = `ctl-${randomUUID()}`;
  const onzone = await controlArm(origin, `${run}-on`, "onzone", options.sockets);
  const offzone = await controlArm(origin, `${run}-off`, "offzone", options.sockets);
  return { run, onzone, offzone, verdict: controlVerdict(onzone, offzone) };
}

interface Preflight {
  host: string;
  colo: string;
  control: Awaited<ReturnType<typeof control>>;
  cleanBurst: number;
  rttMs: Distribution | null;
}

function assertNotWorkersDev(host: string) {
  if (!host.endsWith("workers.dev")) return;
  throw new Abort(
    `${host} is a workers.dev subdomain, where caches.default is inert. Every ` +
      "racer would claim, E would equal N, and the run would read as a maximal\n" +
      "alarming result that measured nothing. Deploy to a zone route.",
  );
}

async function preflight(options: RaceOptions, origin: string): Promise<Preflight> {
  const host = new URL(options.base).host;
  assertNotWorkersDev(host);

  const measured = await control(options, origin);
  if (measured.verdict !== "both-scopes-visible") {
    throw new Abort(
      `the key-scope control returned ${measured.verdict}, not both-scopes-visible.\n` +
        `${JSON.stringify(measured, null, 2)}`,
    );
  }
  if (!(options.scope === "onzone" ? measured.onzone : measured.offzone).visible) {
    throw new Abort(`the ${options.scope} key is not colo-visible; nothing to race for`);
  }

  const clients = await openRacers(origin, 20);
  const clean = await Promise.all(
    Array.from({ length: 200 }, (_, i) => call<Identity>(clients[i % 20]!, "GET", "/identity")),
  );

  const colos = new Set(clean.map((c) => c.colo));
  if (colos.size !== 1 || clean[0]!.colo === null) {
    throw new Abort(
      `the preflight burst reported colos ${JSON.stringify([...colos])}. A run is about ` +
        "one colo's cache, and neither an absent colo nor two of them can be attributed.",
    );
  }

  const latencies: number[] = [];
  for (let i = 0; i < 40; i += 1) {
    const started = performance.now();
    await call<Identity>(clients[0]!, "GET", "/identity");
    latencies.push(performance.now() - started);
  }
  await closeRacers(clients);

  return {
    host,
    colo: clean[0]!.colo,
    control: measured,
    cleanBurst: clean.length,
    rttMs: distribution(latencies),
  };
}

function assertDiscardsTolerable(options: RaceOptions, discarded: number, attempted: number) {
  if (attempted === 0 || discarded / attempted <= options.maxDiscardRate) return;
  throw new Abort(
    `${discarded} of ${attempted} trials were discarded on transport failures ` +
      `(${((discarded / attempted) * 100).toFixed(1)}%, ceiling ` +
      `${(options.maxDiscardRate * 100).toFixed(1)}%). Each discard also rebuilds the ` +
      "socket pool, so the isolates being sampled moved mid-run. Fix the deploy — a fresh " +
      "zone route takes minutes to reach every edge machine — and re-run.",
  );
}

function assertSomebodyClaimed(zeroClaimTrials: number, where: string) {
  if (zeroClaimTrials === 0) return;
  throw new Abort(
    `${zeroClaimTrials} trial(s) at ${where} had no claim at all on a key nobody had ` +
      "written. The instrument is broken, not the cache.",
  );
}

const raceRoute = (options: RaceOptions, key: string, seq: number, jitterMs = 0) =>
  `/race?key=${key}&seq=${seq}&scope=${options.scope}&ttl=${options.sentinelTtlSeconds}` +
  `&jitter=${jitterMs}`;

async function gapSweep(options: RaceOptions, origin: string) {
  const summaries: GapSummary[] = [];
  const trialsByDelta: Record<number, GapTrial[]> = {};
  let discarded = 0;
  let attempted = 0;

  for (const deltaMs of options.deltas) {
    let racers = await openRacers(origin, options.sockets);
    const trials: GapTrial[] = [];
    let attemptedHere = 0;

    for (let trial = 0; trial < options.trials; trial += 1) {
      const key = `race-${randomUUID()}`;
      const first = trial % racers.length;
      const offset = 1 + (Math.floor(trial / racers.length) % (racers.length - 1));
      const second = (first + offset) % racers.length;
      attemptedHere += 1;

      try {
        const sentA = performance.now();
        const a = raceCall(racers[first]!, raceRoute(options, key, 0));
        await sleep(Math.max(0, deltaMs - (performance.now() - sentA)));
        const sentB = performance.now();
        const b = raceCall(racers[second]!, raceRoute(options, key, 1));
        const [first_, second_] = await Promise.all([a, b]);

        trials.push({
          deltaMs,
          achievedDeltaMs: sentB - sentA,
          a: outcomeOf(first_.response, key, 0, { jitterMs: 0, elapsedMs: first_.elapsedMs }),
          b: outcomeOf(second_.response, key, 1, { jitterMs: 0, elapsedMs: second_.elapsedMs }),
        });
      } catch (error) {
        if (error instanceof Abort) throw error;
        discarded += 1;
        await closeRacers(racers).catch(() => {});
        racers = await openRacers(origin, options.sockets);
      }
    }

    await closeRacers(racers);
    attempted += attemptedHere;
    assertDiscardsTolerable(options, discarded, attempted);
    const summary = summarizeGap(deltaMs, trials, attemptedHere);
    assertSomebodyClaimed(summary.excluded["zero-claims"], `delta ${deltaMs}ms`);
    summaries.push(summary);
    trialsByDelta[deltaMs] = trials;
    console.log(
      `  delta ${String(deltaMs).padStart(5)}ms  decidable ${String(summary.decidable).padStart(4)}` +
        `  second claimed ${summary.secondClaims}` +
        `  rate ${summary.secondClaimRate === null ? "n/a" : summary.secondClaimRate.toFixed(4)}` +
        (summary.trials === summary.attempted ? "" : `  (${summary.attempted} attempted)`),
    );
  }

  return { summaries, trialsByDelta, attempted, discarded, window: chooseWindow(summaries) };
}

function assertJitterDrawn(summary: BurstSummary) {
  const verdict = jitterVerdict(summary.jitterMs, summary.delayMs);
  if (verdict !== "degenerate") return;
  throw new Abort(
    `at N=${summary.size}, J=${summary.jitterMs}ms the racers' drawn delays were ` +
      `${JSON.stringify(summary.delayMs)}, which does not cover the window. The worker is ` +
      "not jittering; every escape count in this run is about an un-jittered burst.",
  );
}

const poolRedrawTrials = 25;

async function burstSweep(options: RaceOptions, origin: string) {
  const summaries: BurstSummary[] = [];
  const trialsByCell: Record<string, BurstTrial[]> = {};
  let discarded = 0;
  let attempted = 0;

  for (const size of options.sizes) {
    for (const jitterMs of options.jitters) {
      const poolSize = size + options.sockets;
      let racers = await openRacers(origin, poolSize);
      const trials: BurstTrial[] = [];
      let attemptedHere = 0;

      for (let trial = 0; trial < options.trials; trial += 1) {
        if (trial > 0 && trial % poolRedrawTrials === 0) {
          await closeRacers(racers).catch(() => {});
          racers = await openRacers(origin, poolSize);
        }
        const key = `race-${randomUUID()}`;
        const offset = trial % poolSize;
        const burst = Array.from(
          { length: size },
          (_, i) => racers[(offset + i) % poolSize]!,
        );
        const paths = burst.map((_, seq) => raceRoute(options, key, seq, jitterMs));
        attemptedHere += 1;
        sentAtByPath.clear();
        try {
          const timed = await Promise.all(burst.map((client, seq) => raceCall(client, paths[seq]!)));
          trials.push({
            size,
            jitterMs,
            dispersionMs: dispersionOf(paths),
            outcomes: timed.map(({ response, elapsedMs }, seq) =>
              outcomeOf(response, key, seq, { jitterMs, elapsedMs }),
            ),
          });
        } catch (error) {
          if (error instanceof Abort) throw error;
          discarded += 1;
          await closeRacers(racers).catch(() => {});
          racers = await openRacers(origin, poolSize);
        }
      }

      await closeRacers(racers);
      attempted += attemptedHere;
      assertDiscardsTolerable(options, discarded, attempted);
      const summary = summarizeBurst(size, trials, options.windowMs, attemptedHere, jitterMs);
      assertSomebodyClaimed(summary.discarded["zero-claims"], `N=${size}, J=${jitterMs}ms`);
      assertJitterDrawn(summary);
      summaries.push(summary);
      trialsByCell[`${size}@${jitterMs}`] = trials;
      console.log(
        `  N=${String(size).padStart(4)} J=${String(jitterMs).padStart(5)}ms` +
          `  counted ${String(summary.counted).padStart(4)}` +
          `  escapes median ${summary.escapes?.median ?? "n/a"}` +
          ` (p10 ${summary.escapes?.p10 ?? "n/a"}, p90 ${summary.escapes?.p90 ?? "n/a"},` +
          ` max ${summary.escapes?.max ?? "n/a"}, mean ${summary.escapes?.mean.toFixed(2) ?? "n/a"})` +
          `  late median ${summary.lateEscapes?.median ?? "n/a"}` +
          `  isolates median ${summary.distinctIsolates?.median ?? "n/a"}` +
          `  dispersion median ${summary.dispersionMs?.median.toFixed(2) ?? "n/a"}ms` +
          ` p90 ${summary.dispersionMs?.p90.toFixed(2) ?? "n/a"}ms` +
          (summary.trials === summary.attempted ? "" : `  (${summary.attempted} attempted)`) +
          (summary.lowerBound ? "  [LOWER BOUND]" : ""),
      );
      if ((summary.singleIsolateRate ?? 0) > 0.1) {
        console.log(
          `        ${summary.singleIsolateTrials}/${summary.trials} trials landed on ONE ` +
            "isolate: that fraction measured L0, not L1.",
        );
      }
    }
  }

  return { summaries, trialsByCell, attempted, discarded };
}

interface Report {
  startedAt: string;
  options: RaceOptions;
  control?: Awaited<ReturnType<typeof control>>;
  preflight?: Preflight;
  gap?: Awaited<ReturnType<typeof gapSweep>> & { window: WindowResult };
  burst?: Awaited<ReturnType<typeof burstSweep>>;
  sizing?: { isolatesPerColo: number; rows: SizingRow[] };
}

async function main() {
  const options = parseRaceOptions(
    process.argv.slice(2),
    resolve(dirname(fileURLToPath(import.meta.url)), `../runs/race-${Date.now()}.json`),
  );
  const origin = new URL(options.base).origin;

  const report: Report = { startedAt: new Date().toISOString(), options };

  if (options.phase === "control") {
    assertNotWorkersDev(new URL(options.base).host);
    const measured = await control(options, origin);
    report.control = measured;
    console.log(`\nkey-scope control: ${measured.verdict}`);
    for (const arm of [measured.onzone, measured.offzone]) {
      console.log(
        `  ${arm.scope.padEnd(8)} verified ${arm.verified}` +
          `  cross-isolate hits ${arm.crossIsolateHits}/${arm.crossIsolateReads}` +
          `  reader isolates ${arm.readerIsolates}  colo ${arm.writerColo}`,
      );
    }
    if (measured.verdict === "offzone-inert") {
      console.log(
        "\n  STOP. The on-zone key stores and is colo-visible; the production key shape\n" +
          "  (https://refresh.ocel/…) is not. Every colo-cache tier in workers/nextjs is\n" +
          "  keyed on a synthetic hostname, so L1, the entry tier, the tag-clock front and\n" +
          "  the image tier are all no-ops in production — silently, failing open.",
      );
    }
  } else {
    const checks = await preflight(options, origin);
    report.preflight = checks;
    console.log(
      `\npreflight ok: ${checks.host} in ${checks.colo}, ${checks.cleanBurst} clean requests, ` +
        `rtt median ${checks.rttMs?.median.toFixed(1)}ms (p10 ${checks.rttMs?.p10.toFixed(1)}ms)`,
    );

    if (options.phase === "gap") {
      console.log(`\ngap sweep (${options.trials} trials per delta, scope ${options.scope})`);
      const sweep = await gapSweep(options, origin);
      report.gap = sweep;
      console.log(
        `\nwindow: ${sweep.window.verdict}` +
          (sweep.window.windowMs === null ? "" : ` at ${sweep.window.windowMs}ms`) +
          `  (discarded ${sweep.discarded} of ${sweep.attempted} attempted trials)`,
      );
    } else {
      console.log(`\nburst (${options.trials} trials per size, scope ${options.scope})`);
      const sweep = await burstSweep(options, origin);
      report.burst = sweep;
      console.log(`\ndiscarded ${sweep.discarded} of ${sweep.attempted} attempted trials`);

      const touched = Math.max(0, ...sweep.summaries.map((s) => s.distinctIsolates?.max ?? 0));
      const isolatesPerColo = Math.max(touched, options.isolatesPerColo ?? 0);
      const jittered = options.jitters.some((jitterMs) => jitterMs > 0);
      if (jittered) {
        console.log(
          "\nno sizing table: this sweep jittered, and E = 1 + lambda*W models the\n" +
            "un-jittered path. The jittered bound is 1 + I_colo*W/J and takes no lambda.",
        );
      } else if (options.windowMs !== null) {
        const table = sizingTable(
          {
            windowSeconds: options.windowMs / 1_000,
            isolatesPerColo,
            colos: options.colos,
            sentinelTtlSeconds: options.sentinelTtlSeconds,
          },
          [1, 10, 100, 1_000],
        );
        report.sizing = { isolatesPerColo, rows: table };
        console.log(
          `\nL2 sizing at W=${options.windowMs}ms, I_colo>=${isolatesPerColo}` +
            (isolatesPerColo === touched ? "" : ` (--isolates, above this run's ${touched})`) +
            `, C=${options.colos}, ttl=${options.sentinelTtlSeconds}s ` +
            "(lambda is an OPERATOR PARAMETER, not measured here)",
        );
        for (const row of table) {
          console.log(
            `  lambda ${String(row.lambdaPerColo).padStart(5)} rps  E ${row.escapesPerColo.toFixed(2)}` +
              `${row.cappedByIsolates ? " (capped by isolates)" : ""}` +
              `  F ${row.fanInPerStaleEvent.toFixed(0)}  R ${row.sustainedRequestsPerSecond.toFixed(0)} rps`,
          );
        }
      } else {
        console.log(
          "\nno --window given, so no sizing table: R is a function of W and guessing it\n" +
            "is exactly what this measurement exists to stop.",
        );
      }
    }
  }

  await mkdir(dirname(options.out), { recursive: true });
  await writeFile(options.out, JSON.stringify(report, null, 2));
  console.log(`\nraw observations: ${options.out}`);
}

try {
  await main();
} catch (error) {
  if (!(error instanceof Abort)) throw error;
  console.error(`\nABORTED: ${error.message}\n`);
  process.exitCode = 1;
}
