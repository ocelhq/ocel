import { setTimeout as sleep } from "node:timers/promises";
import { AbortTaskRunError, type RunOptions, task } from "ocel/task";
import { topic } from "ocel/topic";
import { worker } from "ocel/worker";

const ledger = worker("ledger");

const capped = worker("capped", { concurrency: 2 });

type Pace = { n: number; ms: number };

type Span = { n: number; startedAt: number; finishedAt: number };

async function runPaced({ n, ms }: Pace): Promise<Span> {
  const startedAt = Date.now();
  await sleep(ms);
  return { n, startedAt, finishedAt: Date.now() };
}

function describeRun({ ctx }: RunOptions) {
  return {
    kind: ctx.kind,
    name: ctx.name,
    topic: ctx.topic,
    attempt: ctx.attempt.number,
    of: ctx.attempt.of,
  };
}

export const echo = task("echo", {
  run: (payload: unknown, options) => ({ payload, ...describeRun(options) }),
});

export const echoName = task("echo-name", {
  run: (payload: unknown, options) => ({ payload, ...describeRun(options) }),
});

type Flaky = { failures: number; abort?: boolean };

export const flaky = task("flaky", {
  retry: { maxAttempts: 3, minDelay: "1s", maxDelay: "2s" },
  run: ({ failures, abort }: Flaky, { ctx }) => {
    if (abort) {
      throw new AbortTaskRunError("told to abort");
    }
    if (ctx.attempt.number <= failures) {
      throw new Error(`attempt ${ctx.attempt.number} told to fail`);
    }
    return { attempt: ctx.attempt.number, of: ctx.attempt.of };
  },
});

type InOrder = Pace & { failFirst?: boolean };

export const sequence = task("sequence", {
  ordered: true,
  retry: { maxAttempts: 3, minDelay: "1s", maxDelay: "1s" },
  run: async (payload: InOrder, { ctx }) => {
    const span = await runPaced(payload);
    if (payload.failFirst && ctx.attempt.number === 1) {
      throw new Error("the first attempt is told to fail");
    }
    return span;
  },
});

type TallyEntry = { n: number; probe?: string; poison?: boolean };

const poisonFailed = new Set<string>();

export const tally = task("tally", {
  batch: { size: 5, timeout: "2s" },
  retry: { maxAttempts: 3, minDelay: "1s", maxDelay: "1s" },
  run: async (entries: TallyEntry[], { ctx }) => {
    const batch = entries.map((entry) => entry.n);
    const poisoned = entries
      .filter((entry) => entry.poison)
      .map((entry) => `${entry.probe}/${entry.n}`)
      .filter((key) => !poisonFailed.has(key));
    for (const key of poisoned) {
      poisonFailed.add(key);
    }
    const failed = poisoned.length > 0;
    for (const probe of new Set(entries.flatMap((entry) => (entry.probe ? [entry.probe] : [])))) {
      await receipt.trigger({ probe, batch, failed }, { tags: [probe] });
    }
    if (failed) {
      throw new Error("a batch fails the first time it holds a poisoned entry");
    }
    return { batch, attempt: ctx.attempt.number };
  },
});

export const laned = task("laned", {
  concurrency: 1,
  run: runPaced,
});

export const limited = task("limited", {
  concurrency: 2,
  run: runPaced,
});

export const alpha = task("alpha", {
  worker: capped,
  concurrency: 10,
  run: runPaced,
});

export const beta = task("beta", {
  worker: capped,
  concurrency: 10,
  run: runPaced,
});

type Ticking = { probe: string; ms: number };

const TICK_MS = 200;

export const ticking = task("ticking", {
  run: async ({ probe, ms }: Ticking, { signal }) => {
    const until = Date.now() + ms;
    let ticks = 0;
    while (Date.now() < until && !signal.aborted) {
      ticks += 1;
      await receipt.trigger({ probe, tick: ticks }, { tags: [probe] });
      await sleep(TICK_MS);
    }
    return { ticks, stopped: signal.aborted };
  },
});

export const outlives = task("outlives", {
  maxDuration: "1s",
  retry: { maxAttempts: 3, minDelay: "1s", maxDelay: "1s" },
  run: runPaced,
});

export const heartbeat = task("heartbeat", {
  cron: "* * * * *",
  run: (payload: { timestamp: string }) => payload,
});

type ProbeMessage = { probe: string } & Record<string, unknown>;

export const receipt = task("receipt", {
  run: (payload: ProbeMessage) => payload,
});

async function recordReceipt(payload: ProbeMessage, options: RunOptions): Promise<void> {
  await receipt.trigger(
    { ...payload, ...describeRun(options), worker: process.env.OCEL_WORKER ?? "" },
    { tags: [payload.probe] },
  );
}

export const orders = topic<ProbeMessage>("orders");

export const auditLog = orders.consumer("audit-log", recordReceipt);

export const ledgerEntry = orders.consumer("ledger-entry", recordReceipt, {
  worker: ledger,
});

export const doomed = topic<ProbeMessage>("doomed");

export const alwaysFails = doomed.consumer(
  "always-fails",
  () => {
    throw new Error("this consumer fails every attempt");
  },
  { retry: { maxAttempts: 2, minDelay: "1s", maxDelay: "1s" } },
);

export const stalls = topic<Pace>("stalls");

export const tooSlow = stalls.consumer("too-slow", runPaced, {
  maxDuration: "1s",
  retry: { maxAttempts: 2, minDelay: "1s", maxDelay: "1s" },
});

export const tasks = {
  echo,
  "echo-name": echoName,
  flaky,
  sequence,
  tally,
  laned,
  limited,
  alpha,
  beta,
  ticking,
  outlives,
  heartbeat,
  receipt,
};

export const topics = {
  orders,
  doomed,
  stalls,
};
