// Drives the deployed probe and writes down what it saw. Run it from a machine
// outside Cloudflare: every elapsed time is measured against this process's
// clock, which is the only clock in the system that is allowed to be differenced.
//
//   node scripts/probe.ts --base https://probe.example.com
//
// See README.md for the full flag set and for why the base URL must not be a
// workers.dev subdomain.

import { mkdir, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { Agent, type Dispatcher } from "undici";

import {
  showsCrossIsolateVisibility,
  summarizeIsolates,
  summarizeSentinel,
  summarizeTtl,
  type IdentitySample,
  type SentinelObservation,
  type TtlObservation,
} from "../src/analysis.ts";
import { parseArgs, type Options } from "../src/options.ts";
import { sleep } from "../src/race.ts";


async function getJson<T>(url: string, init?: RequestInit & { dispatcher?: Dispatcher }) {
  const response = await fetch(url, init);
  if (!response.ok) {
    throw new Error(`${init?.method ?? "GET"} ${url} -> ${response.status}`);
  }
  return (await response.json()) as T;
}

const burst = <T>(count: number, call: () => Promise<T>) =>
  Promise.all(Array.from({ length: count }, call));

interface Identity {
  isolate: string;
  colo: string | null;
  host: string;
}

interface EntryWrite extends Identity {
  sentinel: { writer: string; ttlSeconds: number };
  verified: boolean;
  verifiedCacheControl: string | null;
}

interface EntryRead extends Identity {
  hit: boolean;
  writer: string | null;
  age: string | null;
  cacheControl: string | null;
}

async function census(options: Options): Promise<IdentitySample[]> {
  const samples: IdentitySample[] = [];
  for (let round = 0; round < options.rounds; round += 1) {
    // A fresh dispatcher per round: undici pools keep-alive sockets per origin,
    // so reusing the default one would replay round 1's connections and add
    // little sampling surface beyond it.
    const dispatcher = new Agent();
    try {
      const identities = await burst(options.concurrency, () =>
        getJson<Identity>(`${options.base}/identity`, { dispatcher }),
      );
      samples.push(
        ...identities.map((i) => ({ colo: i.colo ?? "unknown", isolate: i.isolate })),
      );
    } finally {
      await dispatcher.close();
    }
    await sleep(250);
  }
  return samples;
}

async function sentinel(options: Options, run: string) {
  const written = await getJson<EntryWrite>(
    `${options.base}/entry?run=${run}&ttl=${options.sentinelSeconds}`,
    { method: "PUT" },
  );
  const writtenAt = Date.now();

  const observations: SentinelObservation[] = [];
  // Read for a while: the answer being looked for is whether a *later* isolate
  // sees the write, so the burst has to be wide enough to land somewhere else
  // and repeated long enough for any propagation to finish.
  for (let round = 0; round < options.rounds * 2; round += 1) {
    const reads = await burst(options.concurrency, async () => {
      // Stamped per read, not per burst: a burst-wide stamp would give every
      // read the slowest read's latency and overstate the propagation delay.
      const read = await getJson<EntryRead>(`${options.base}/entry?run=${run}`);
      return { read, elapsedMs: Date.now() - writtenAt };
    });
    observations.push(
      ...reads.map(({ read, elapsedMs }) => ({
        reader: read.isolate,
        colo: read.colo ?? "unknown",
        writer: read.writer,
        hit: read.hit,
        elapsedMs,
      })),
    );
    await sleep(500);
  }

  return {
    writer: written.isolate,
    writerColo: written.colo,
    verifiedAtWrite: written.verified,
    verifiedCacheControl: written.verifiedCacheControl,
    observations,
    summary: summarizeSentinel(written.isolate, written.colo, observations),
  };
}

async function ttl(
  options: Options,
  run: string,
  ttlSeconds: number,
  crossIsolateVisible: boolean,
) {
  const written = await getJson<EntryWrite>(
    `${options.base}/entry?run=${run}&ttl=${ttlSeconds}`,
    { method: "PUT" },
  );
  const writtenAt = Date.now();

  const observations: TtlObservation[] = [];
  const deadline = writtenAt + options.windowSeconds * 1_000;
  let consecutiveMisses = 0;
  while (Date.now() < deadline) {
    await sleep(options.pollSeconds * 1_000);
    const reads = await burst(options.pollFanout, () =>
      getJson<EntryRead>(`${options.base}/entry?run=${run}`),
    );
    observations.push({
      elapsedMs: Date.now() - writtenAt,
      reads: reads.map((read) => ({
        isolate: read.isolate,
        hit: read.hit,
        ageSeconds: read.age === null ? null : Number(read.age),
        cacheControl: read.cacheControl,
      })),
    });

    // Only a read that could have seen the entry counts toward stopping. If the
    // cache turns out to be isolate-local, a fan-out that missed the writer's
    // isolate says nothing about the entry's lifetime, and stopping on it would
    // report an eviction that never happened.
    const counted = crossIsolateVisible
      ? reads
      : reads.filter((read) => read.isolate === written.isolate);
    if (!counted.length) continue;
    // Two consecutive misses end the poll: one miss could be a read that raced
    // an eviction that a later read disproves, and the analysis treats it that
    // way, so the run stops only once the entry looks durably gone.
    consecutiveMisses = counted.some((read) => read.hit) ? 0 : consecutiveMisses + 1;
    if (consecutiveMisses >= 2) break;
  }

  return {
    ttlSeconds,
    verifiedAtWrite: written.verified,
    verifiedCacheControl: written.verifiedCacheControl,
    observations,
    summary: summarizeTtl(ttlSeconds, written.isolate, observations, crossIsolateVisible),
  };
}

async function main() {
  const options = parseArgs(
    process.argv.slice(2),
    resolve(dirname(fileURLToPath(import.meta.url)), "../runs/probe.json"),
  );
  const host = new URL(options.base).host;
  if (host.endsWith("workers.dev")) {
    console.warn(
      `WARNING: ${host} is a workers.dev subdomain, where the Cache API is a no-op.\n` +
        "         Every measurement below will read as never-cached regardless of\n" +
        "         Cloudflare's real behaviour. Deploy to a zone route first.\n",
    );
  }

  const runId = `probe-${Date.now()}`;
  const startedAt = new Date().toISOString();
  const isolates = summarizeIsolates(await census(options));
  const measured = await sentinel(options, `${runId}-sentinel`);
  const crossIsolateVisible = showsCrossIsolateVisibility(measured.summary.verdict);
  const report = {
    runId,
    base: options.base,
    startedAt,
    options,
    isolates,
    sentinel: measured,
    ttl: [] as Awaited<ReturnType<typeof ttl>>[],
  };
  for (const seconds of options.ttls) {
    report.ttl.push(await ttl(options, `${runId}-ttl-${seconds}`, seconds, crossIsolateVisible));
  }

  await mkdir(dirname(options.out), { recursive: true });
  await writeFile(options.out, JSON.stringify(report, null, 2));

  console.log(`\nisolates per colo`);
  for (const row of report.isolates) {
    console.log(`  ${row.colo}: ${row.isolates} isolates over ${row.samples} requests`);
  }
  console.log(
    `\nsentinel (writer ${measured.writer} in ${measured.writerColo}, ` +
      `readable by its own isolate at write: ${measured.verifiedAtWrite})`,
  );
  console.log(`  ${JSON.stringify(measured.summary, null, 2).replace(/\n/g, "\n  ")}`);
  for (const run of report.ttl) {
    console.log(`\nttl ${run.ttlSeconds}s (readable at write: ${run.verifiedAtWrite})`);
    console.log(`  ${JSON.stringify(run.summary, null, 2).replace(/\n/g, "\n  ")}`);
  }
  console.log(`\nraw observations: ${options.out}`);
}

await main();
