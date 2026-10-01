import { setTimeout as sleep } from "node:timers/promises";
import { AbortTaskRunError, type RunOptions, task } from "ocel/task";
import { topic } from "ocel/topic";
import { worker } from "ocel/worker";

const ledger = worker("ledger");

const capped = worker("capped", { concurrency: 2 });

type Paced = { n: number; ms: number };

type Span = { n: number; startedAt: number; finishedAt: number };

async function runPaced({ n, ms }: Paced): Promise<Span> {
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

type InOrder = Paced & { failFirst?: boolean };

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

type Tallied = { n: number; poison?: boolean };

export const tally = task("tally", {
  batch: { size: 5, timeout: "2s" },
  retry: { maxAttempts: 3, minDelay: "1s", maxDelay: "1s" },
  run: (payloads: Tallied[], { ctx }) => {
    if (ctx.attempt.number === 1 && payloads.some((one) => one.poison)) {
      throw new Error("a batch holding a poisoned item fails its first attempt");
    }
    return { batch: payloads.map((one) => one.n), attempt: ctx.attempt.number };
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

export const outlives = task("outlives", {
  maxDuration: "1s",
  retry: { maxAttempts: 3, minDelay: "1s", maxDelay: "1s" },
  run: runPaced,
});

export const heartbeat = task("heartbeat", {
  cron: "* * * * *",
  run: (payload: { timestamp: string }) => payload,
});

type Received = { probe: string } & Record<string, unknown>;

export const receipt = task("receipt", {
  run: (payload: Received) => payload,
});

async function recordReceipt(payload: Received, options: RunOptions): Promise<void> {
  await receipt.trigger(
    { ...payload, ...describeRun(options), worker: process.env.OCEL_WORKER ?? "" },
    { tags: [payload.probe] },
  );
}

export const orders = topic<Received>("orders");

export const auditLog = orders.consumer("audit-log", recordReceipt);

export const ledgerEntry = orders.consumer("ledger-entry", recordReceipt, {
  worker: ledger,
});

export const doomed = topic<Received>("doomed");

export const alwaysFails = doomed.consumer(
  "always-fails",
  () => {
    throw new Error("this consumer fails every attempt");
  },
  { retry: { maxAttempts: 2, minDelay: "1s", maxDelay: "1s" } },
);

export const stalls = topic<Paced>("stalls");

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
  outlives,
  heartbeat,
  receipt,
};

export const topics = {
  orders,
  doomed,
  stalls,
};
