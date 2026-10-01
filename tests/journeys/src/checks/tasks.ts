import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { setTimeout as delay } from "node:timers/promises";
import { type Check, type CheckContext, json } from "./context";
import {
  describeReply,
  ENDED,
  type Reply,
  type RunRecord,
  readAnswer,
  SETTLED_WITHIN_MS,
  waitFor,
} from "./runs";

function post(ctx: CheckContext, at: string, body: unknown = {}): Promise<Reply> {
  return json(ctx, at, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  });
}

function newProbe(of: string): string {
  return `${of}-${randomUUID()}`;
}

async function trigger(
  ctx: CheckContext,
  task: string,
  payload: unknown,
  options: Record<string, unknown> = {},
): Promise<string> {
  const sent = await post(ctx, `/api/tasks/${task}/trigger`, { payload, options });
  return readAnswer<{ id: string }>(sent, `a trigger of ${task}`).id;
}

async function retrieve(ctx: CheckContext, id: string): Promise<RunRecord> {
  return readAnswer<RunRecord>(await json(ctx, `/api/runs/${id}`), `the retrieve of ${id}`);
}

async function waitForRun(
  ctx: CheckContext,
  id: string,
  statuses: string[] = ENDED,
  withinMs = SETTLED_WITHIN_MS,
): Promise<RunRecord> {
  let run: RunRecord | undefined;
  return waitFor(
    () =>
      `run ${id} is still ${run?.status} after ${withinMs}ms, not ${statuses.join(" or ")}: ${JSON.stringify(run)}`,
    async () => {
      run = await retrieve(ctx, id);
      return statuses.includes(run.status) ? run : undefined;
    },
    withinMs,
  );
}

function assertCompleted(run: RunRecord): void {
  assert.equal(run.status, "COMPLETED", `run ${run.id} ended ${run.status}: ${run.error}`);
}

async function assertEchoed(ctx: CheckContext, task: string): Promise<void> {
  const payload = { probe: newProbe(task), nested: { list: [1, "two"] } };
  const id = await trigger(ctx, task, payload);
  const run = await waitForRun(ctx, id);
  assertCompleted(run);
  assert.equal(run.task, task);
  assert.equal(run.attempts, 1);
  assert.deepEqual(run.payload, payload);
  assert.deepEqual(run.output, {
    payload,
    kind: "task",
    name: task,
    topic: task,
    attempt: 1,
    of: 3,
  });
}

export const triggeredRunCheck: Check = {
  title:
    "a triggered run completes with its payload and output, under the name the task was declared with",
  run: async (ctx) => {
    await assertEchoed(ctx, "echo");
  },
};

export const hyphenatedTaskCheck: Check = {
  title: "a task whose declared name holds a hyphen is bound in the app and runs under that name",
  run: async (ctx) => {
    await assertEchoed(ctx, "echo-name");
  },
};

export const retryCheck: Check = {
  title: "a failed attempt is retried until one succeeds, and the run counts every attempt",
  run: async (ctx) => {
    const run = await waitForRun(ctx, await trigger(ctx, "flaky", { failures: 2 }));
    assertCompleted(run);
    assert.equal(run.attempts, 3);
    assert.deepEqual(run.output, { attempt: 3, of: 3 });
  },
};

export const retriesExhaustedCheck: Check = {
  title: "a run whose every attempt fails ends FAILED with the last attempt's error",
  run: async (ctx) => {
    const run = await waitForRun(ctx, await trigger(ctx, "flaky", { failures: 5 }));
    assert.equal(run.status, "FAILED");
    assert.equal(run.attempts, 3);
    assert.match(run.error ?? "", /attempt 3 told to fail/);
  },
};

export const triggerLowersMaxAttemptsCheck: Check = {
  title: "a trigger's maxAttempts lowers the task's",
  run: async (ctx) => {
    const run = await waitForRun(
      ctx,
      await trigger(ctx, "flaky", { failures: 5 }, { maxAttempts: 1 }),
    );
    assert.equal(run.status, "FAILED");
    assert.equal(run.attempts, 1);
  },
};

export const abortCheck: Check = {
  title: "an AbortTaskRunError fails the run at once, without another attempt",
  run: async (ctx) => {
    const run = await waitForRun(ctx, await trigger(ctx, "flaky", { failures: 0, abort: true }));
    assert.equal(run.status, "FAILED");
    assert.equal(run.attempts, 1);
    assert.match(run.error ?? "", /told to abort/);
  },
};

const DELAY_MS = 3_000;
const CLOCK_SLACK_MS = 1_000;
const STARTS_WITHIN_MS = 10_000;

export const delayCheck: Check = {
  title: "a delayed run waits DELAYED until it is due, and starts soon after, never before",
  run: async (ctx) => {
    const id = await trigger(ctx, "echo", { probe: newProbe("delay") }, { delay: "3s" });
    const waiting = await retrieve(ctx, id);
    assert.equal(waiting.status, "DELAYED");
    const scheduledMs = (waiting.dueAt ?? 0) - (waiting.createdAt ?? 0);
    assert.ok(
      Math.abs(scheduledMs - DELAY_MS) <= CLOCK_SLACK_MS,
      `the run is due ${scheduledMs}ms after it was triggered, not ${DELAY_MS}ms`,
    );
    const run = await waitForRun(ctx, id);
    assertCompleted(run);
    const lateMs = (run.startedAt ?? 0) - (run.dueAt ?? 0);
    assert.ok(lateMs >= 0, `the run started ${-lateMs}ms before it was due`);
    assert.ok(lateMs <= STARTS_WITHIN_MS, `the run started ${lateMs}ms after it was due`);
  },
};

const DELAY_BOUND_MS = 30 * 24 * 60 * 60 * 1_000;
const PAST_THE_BOUND_MS = 60_000;

export const delayBoundCheck: Check = {
  title: "a trigger due exactly 30 days out is taken, and one due a minute past 30 days is refused",
  run: async (ctx) => {
    const id = await trigger(ctx, "echo", {}, { delay: "30d" });
    assert.equal((await retrieve(ctx, id)).status, "DELAYED");
    assert.equal(
      readAnswer<RunRecord>(await post(ctx, `/api/runs/${id}/cancel`), "the cancel").status,
      "CANCELED",
    );
    const dueAt = new Date(Date.now() + DELAY_BOUND_MS + PAST_THE_BOUND_MS).toISOString();
    const refused = await post(ctx, "/api/tasks/echo/trigger", { payload: {}, options: { dueAt } });
    assert.notEqual(
      refused.res.status,
      200,
      `a run due a minute past 30 days answered ${describeReply(refused)}`,
    );
    assert.match(describeReply(refused), /30 days/);
  },
};

const TTL_BLOCKER_MS = 4_000;

export const ttlExpiryCheck: Check = {
  title: "a run that waits longer than its ttl to start ends EXPIRED without an attempt",
  run: async (ctx) => {
    const blocker = await trigger(ctx, "laned", { n: 0, ms: TTL_BLOCKER_MS });
    await waitForRun(ctx, blocker, ["EXECUTING", ...ENDED]);
    const id = await trigger(ctx, "laned", { n: 1, ms: 0 }, { ttl: "1s" });
    const run = await waitForRun(ctx, id);
    assert.equal(run.status, "EXPIRED");
    assert.equal(run.attempts, 0);
    assertCompleted(await waitForRun(ctx, blocker));
  },
};

export const idempotencyCheck: Check = {
  title: "a trigger repeating a remembered idempotency key returns the original run",
  run: async (ctx) => {
    const key = newProbe("idempotency");
    const first = await trigger(ctx, "echo", { n: 1 }, { idempotencyKey: key });
    const again = await trigger(ctx, "echo", { n: 2 }, { idempotencyKey: key });
    assert.equal(again, first);
    const run = await waitForRun(ctx, first);
    assertCompleted(run);
    assert.deepEqual(run.payload, { n: 1 });
    const other = await trigger(ctx, "echo", { n: 3 }, { idempotencyKey: newProbe("other") });
    assert.notEqual(other, first);
  },
};

export const idempotencyKeyTtlCheck: Check = {
  title: "an idempotency key is free again once its ttl passes",
  run: async (ctx) => {
    const options = { idempotencyKey: newProbe("expiring"), idempotencyKeyTTL: "1s" };
    const first = await trigger(ctx, "echo", { n: 1 }, options);
    await delay(2_000);
    assert.notEqual(await trigger(ctx, "echo", { n: 2 }, options), first);
  },
};

const DEBOUNCE_MS = 2_000;
const DEBOUNCE_SPACING_MS = 500;

export const debounceCheck: Check = {
  title:
    "triggers sharing a debounce key fold into the first one's run, due the delay after the last",
  run: async (ctx) => {
    const debounce = { key: newProbe("debounce"), delay: "2s" };
    const ids: string[] = [];
    for (const n of [1, 2, 3]) {
      ids.push(await trigger(ctx, "echo", { n }, { debounce }));
      await delay(DEBOUNCE_SPACING_MS);
    }
    assert.deepEqual(new Set(ids).size, 1, `the triggers started ${ids.join(", ")}`);
    const run = await waitForRun(ctx, ids[0] ?? "");
    assertCompleted(run);
    assert.deepEqual(run.payload, { n: 1 });
    const foldedMs = (run.dueAt ?? 0) - (run.createdAt ?? 0);
    assert.ok(
      foldedMs >= DEBOUNCE_MS + 2 * DEBOUNCE_SPACING_MS - CLOCK_SLACK_MS,
      `the run was due ${foldedMs}ms after the first trigger, not the delay after the last`,
    );
  },
};

type Span = { n: number; startedAt: number; finishedAt: number };

async function triggerAll(
  ctx: CheckContext,
  task: string,
  payloads: unknown[],
  options: Record<string, unknown> = {},
): Promise<string[]> {
  const ids: string[] = [];
  for (const payload of payloads) {
    ids.push(await trigger(ctx, task, payload, options));
  }
  return ids;
}

async function waitForSpans(ctx: CheckContext, ids: string[]): Promise<Span[]> {
  const spans: Span[] = [];
  for (const id of ids) {
    const run = await waitForRun(ctx, id);
    assertCompleted(run);
    spans.push(run.output as Span);
  }
  return spans;
}

export function findMostAtOnce(spans: Span[]): number {
  const edges = spans.flatMap((span) => [
    { at: span.startedAt, by: 1 },
    { at: span.finishedAt, by: -1 },
  ]);
  edges.sort((a, b) => a.at - b.at || a.by - b.by);
  let running = 0;
  let most = 0;
  for (const edge of edges) {
    running += edge.by;
    most = Math.max(most, running);
  }
  return most;
}

const PACED_MS = 300;

export const orderedPerKeyCheck: Check = {
  title:
    "an ordered task runs one run per key at a time, in trigger order, and a failing run holds its key until it succeeds",
  run: async (ctx) => {
    const key = newProbe("key");
    const ids = await triggerAll(
      ctx,
      "sequence",
      [1, 2, 3, 4].map((n) => ({ n, ms: PACED_MS, failFirst: n === 1 })),
      { key },
    );
    const spans = await waitForSpans(ctx, ids);
    assert.equal((await retrieve(ctx, ids[0] ?? "")).attempts, 2);
    for (const [i, span] of spans.entries()) {
      const next = spans[i + 1];
      if (next) {
        assert.ok(
          next.startedAt >= span.finishedAt,
          `run ${next.n} started ${span.finishedAt - next.startedAt}ms before run ${span.n} of its key finished`,
        );
      }
    }
  },
};

export const orderedKeysInParallelCheck: Check = {
  title: "an ordered task runs different keys at the same time",
  run: async (ctx) => {
    const ids = await triggerAll(
      ctx,
      "sequence",
      [1, 2, 3].map((n) => ({ n, ms: 1_500 })),
    );
    const keyed = await Promise.all(
      [4, 5, 6].map((n) => trigger(ctx, "sequence", { n, ms: 1_500 }, { key: newProbe("own") })),
    );
    assert.ok(findMostAtOnce(await waitForSpans(ctx, [...ids, ...keyed])) > 1);
  },
};

type Tally = { batch: number[]; attempt: number };

export const batchCheck: Check = {
  title: "a batch task receives triggers together, up to its size",
  run: async (ctx) => {
    const sent = await post(ctx, "/api/tasks/tally/batch-trigger", {
      items: [1, 2, 3, 4, 5, 6, 7].map((n) => ({ payload: { n } })),
    });
    const { ids } = readAnswer<{ ids: string[] }>(sent, "the batch trigger");
    assert.equal(ids.length, 7);
    const batches: number[][] = [];
    for (const id of ids) {
      const run = await waitForRun(ctx, id);
      assertCompleted(run);
      const { batch } = run.output as Tally;
      assert.ok(batch.length <= 5, `one attempt received ${batch.length} runs, over the size of 5`);
      batches.push(batch);
    }
    assert.ok(
      batches.some((batch) => batch.length > 1),
      `every run arrived alone: ${JSON.stringify(batches)}`,
    );
  },
};

export const batchRetryCheck: Check = {
  title: "a failed batch retries each of its runs, every one counting its own attempts",
  run: async (ctx) => {
    const sent = await post(ctx, "/api/tasks/tally/batch-trigger", {
      items: [{ payload: { n: 1 } }, { payload: { n: 2, poison: true } }, { payload: { n: 3 } }],
    });
    const { ids } = readAnswer<{ ids: string[] }>(sent, "the batch trigger");
    for (const [i, id] of ids.entries()) {
      const run = await waitForRun(ctx, id);
      assertCompleted(run);
      assert.equal(run.attempts, 2, `run ${i + 1} took ${run.attempts} attempts`);
      const { batch, attempt } = run.output as Tally;
      assert.equal(attempt, 2);
      assert.ok(batch.includes(i + 1), `run ${i + 1} completed in a batch of ${batch}`);
    }
  },
};

const LANE_RUNS = 6;

function findMeanRank(spans: Span[], of: number[]): number {
  const ranked = [...spans].sort((a, b) => a.startedAt - b.startedAt).map((span) => span.n);
  return of.reduce((sum, n) => sum + ranked.indexOf(n), 0) / of.length;
}

export const lanesCheck: Check = {
  title:
    "runs waiting in the high lane are read ahead of those in the low lane, and low still runs",
  run: async (ctx) => {
    const blocker = await trigger(ctx, "laned", { n: 0, ms: 3_000 });
    await waitForRun(ctx, blocker, ["EXECUTING", ...ENDED]);
    const low = Array.from({ length: LANE_RUNS }, (_, i) => i + 1);
    const high = low.map((n) => n + LANE_RUNS);
    const ids = [
      ...(await triggerAll(
        ctx,
        "laned",
        low.map((n) => ({ n, ms: 50 })),
        { lane: "low" },
      )),
      ...(await triggerAll(
        ctx,
        "laned",
        high.map((n) => ({ n, ms: 50 })),
        { lane: "high" },
      )),
    ];
    const spans = await waitForSpans(ctx, ids);
    const highRank = findMeanRank(spans, high);
    const lowRank = findMeanRank(spans, low);
    assert.ok(
      highRank < lowRank,
      `high runs started at a mean rank of ${highRank}, low ones at ${lowRank}`,
    );
  },
};

const CAPPED_MS = 1_500;

export const taskConcurrencyCheck: Check = {
  title: "a task runs at most its concurrency at once",
  run: async (ctx) => {
    const ids = await triggerAll(
      ctx,
      "limited",
      [1, 2, 3, 4, 5, 6].map((n) => ({ n, ms: CAPPED_MS })),
    );
    assert.equal(findMostAtOnce(await waitForSpans(ctx, ids)), 2);
  },
};

export const workerConcurrencyCheck: Check = {
  title: "a worker runs at most its concurrency at once, across every task on it",
  run: async (ctx) => {
    const ids = [
      ...(await triggerAll(
        ctx,
        "alpha",
        [1, 2, 3].map((n) => ({ n, ms: CAPPED_MS })),
      )),
      ...(await triggerAll(
        ctx,
        "beta",
        [4, 5, 6].map((n) => ({ n, ms: CAPPED_MS })),
      )),
    ];
    assert.equal(findMostAtOnce(await waitForSpans(ctx, ids)), 2);
  },
};

export const maxDurationCheck: Check = {
  title: "a task attempt past its maxDuration ends the run TIMED_OUT, without another attempt",
  run: async (ctx) => {
    const run = await waitForRun(ctx, await trigger(ctx, "outlives", { n: 1, ms: 5_000 }));
    assert.equal(run.status, "TIMED_OUT");
    assert.equal(run.attempts, 1);
  },
};

type RunPage = { runs: RunRecord[]; nextCursor: string };

async function listRuns(ctx: CheckContext, query: Record<string, string>): Promise<RunRecord[]> {
  const sent = await json(ctx, `/api/runs?${new URLSearchParams(query)}`);
  return readAnswer<RunPage>(sent, `the listing of ${JSON.stringify(query)}`).runs;
}

export const listRunsCheck: Check = {
  title: "runs are listed newest first by task, status and tags",
  run: async (ctx) => {
    const tag = newProbe("listed");
    const older = await trigger(ctx, "echo", { n: 1 }, { tags: [tag] });
    const newer = await trigger(ctx, "echo", { n: 2 }, { tags: [tag, "second"] });
    const delayed = await trigger(ctx, "flaky", { failures: 0 }, { tags: [tag], delay: "1h" });
    await waitForRun(ctx, older);
    await waitForRun(ctx, newer);
    const ids = (runs: RunRecord[]) => runs.map((run) => run.id);
    assert.deepEqual(ids(await listRuns(ctx, { tags: tag })), [delayed, newer, older]);
    assert.deepEqual(ids(await listRuns(ctx, { tags: `${tag},second` })), [newer]);
    assert.deepEqual(ids(await listRuns(ctx, { tags: tag, task: "echo" })), [newer, older]);
    assert.deepEqual(ids(await listRuns(ctx, { tags: tag, status: "DELAYED" })), [delayed]);
    assert.deepEqual(ids(await listRuns(ctx, { tags: tag, status: "COMPLETED,DELAYED" })), [
      delayed,
      newer,
      older,
    ]);
    await post(ctx, `/api/runs/${delayed}/cancel`);
  },
};

async function cancel(ctx: CheckContext, id: string): Promise<RunRecord> {
  return readAnswer<RunRecord>(await post(ctx, `/api/runs/${id}/cancel`), `the cancel of ${id}`);
}

export const cancelDelayedCheck: Check = {
  title: "a canceled run that has not started never runs",
  run: async (ctx) => {
    const id = await trigger(ctx, "echo", { n: 1 }, { delay: "2s" });
    assert.equal((await cancel(ctx, id)).status, "CANCELED");
    await delay(4_000);
    const run = await retrieve(ctx, id);
    assert.equal(run.status, "CANCELED");
    assert.equal(run.attempts, 0);
  },
};

export const cancelExecutingCheck: Check = {
  title: "canceling an executing run stops it, and it makes no further attempt",
  run: async (ctx) => {
    const id = await trigger(ctx, "limited", { n: 1, ms: 4_000 });
    await waitForRun(ctx, id, ["EXECUTING", ...ENDED]);
    assert.equal((await cancel(ctx, id)).status, "CANCELED");
    await delay(5_000);
    const run = await retrieve(ctx, id);
    assert.equal(run.status, "CANCELED");
    assert.equal(run.attempts, 1);
  },
};

export const replayCheck: Check = {
  title: "a replay starts a new run with the same payload",
  run: async (ctx) => {
    const payload = { probe: newProbe("replayed") };
    const original = await trigger(ctx, "echo", payload);
    assertCompleted(await waitForRun(ctx, original));
    const replayed = readAnswer<{ id: string }>(
      await post(ctx, `/api/runs/${original}/replay`),
      "the replay",
    ).id;
    assert.notEqual(replayed, original);
    const run = await waitForRun(ctx, replayed);
    assertCompleted(run);
    assert.equal(run.task, "echo");
    assert.deepEqual(run.payload, payload);
  },
};

export const rescheduleCheck: Check = {
  title: "a delayed run rescheduled sooner runs at its new time, and an ended run cannot be",
  run: async (ctx) => {
    const id = await trigger(ctx, "echo", { n: 1 }, { delay: "1h" });
    const dueAt = new Date(Date.now() + 2_000).toISOString();
    const sent = await post(ctx, `/api/runs/${id}/reschedule`, { dueAt });
    const moved = readAnswer<RunRecord>(sent, "the reschedule");
    assert.equal(moved.status, "DELAYED");
    assert.ok(
      Math.abs((moved.dueAt ?? 0) - Date.parse(dueAt)) <= CLOCK_SLACK_MS,
      `the run is due at ${moved.dueAt}, not ${dueAt}`,
    );
    const run = await waitForRun(ctx, id, ENDED, STARTS_WITHIN_MS + 2_000);
    assertCompleted(run);
    const late = await post(ctx, `/api/runs/${id}/reschedule`, { dueAt });
    assert.notEqual(late.res.status, 200, `an ended run was rescheduled: ${describeReply(late)}`);
  },
};

async function send(
  ctx: CheckContext,
  topic: string,
  payload: unknown,
  options: Record<string, unknown> = {},
): Promise<string> {
  const sent = await post(ctx, `/api/topics/${topic}/send`, { payload, options });
  return readAnswer<{ messageId: string }>(sent, `a send to ${topic}`).messageId;
}

type Receipt = {
  probe: string;
  kind: string;
  name: string;
  topic: string;
  worker: string;
  attempt: number;
};

async function waitForReceipts(
  ctx: CheckContext,
  probe: string,
  count: number,
): Promise<Receipt[]> {
  let received = 0;
  return waitFor(
    () => `${received} of ${count} consumers received ${probe}`,
    async () => {
      const runs = await listRuns(ctx, { task: "receipt", tags: probe, status: "COMPLETED" });
      received = runs.length;
      return runs.length >= count
        ? runs.map((run) => run.output as Receipt).sort((a, b) => a.name.localeCompare(b.name))
        : undefined;
    },
  );
}

export const fanOutCheck: Check = {
  title:
    "a message sent to a topic reaches every consumer once, each on its own worker, under the names they were declared with",
  run: async (ctx) => {
    const probe = newProbe("fan-out");
    const messageId = await send(ctx, "orders", { probe });
    assert.match(messageId, /^[0-9A-HJKMNP-TV-Z]{26}$/);
    const receipts = await waitForReceipts(ctx, probe, 2);
    const seen = ({ kind, name, topic, worker }: Receipt) => ({ kind, name, topic, worker });
    assert.deepEqual(receipts.map(seen), [
      { kind: "consumer", name: "audit-log", topic: "orders", worker: "worker" },
      { kind: "consumer", name: "ledger-entry", topic: "orders", worker: "ledger" },
    ]);
    await delay(2_000);
    assert.equal((await waitForReceipts(ctx, probe, 2)).length, 2);
  },
};

export const sendIdempotencyCheck: Check = {
  title: "a send repeating a remembered idempotency key is delivered once",
  run: async (ctx) => {
    const probe = newProbe("sent-once");
    const idempotencyKey = newProbe("send-key");
    const first = await send(ctx, "orders", { probe }, { idempotencyKey });
    assert.equal(await send(ctx, "orders", { probe }, { idempotencyKey }), first);
    await waitForReceipts(ctx, probe, 2);
    await delay(2_000);
    assert.equal((await waitForReceipts(ctx, probe, 2)).length, 2);
  },
};

type DeadLetter = {
  execution: string;
  messageId: string;
  payload: unknown;
  attempts: number;
  error: string;
};

type DeadLetterPage = { count: number; deadLetters: DeadLetter[] };

async function readDeadLetters(
  ctx: CheckContext,
  topic: string,
  consumer: string,
): Promise<DeadLetterPage> {
  const sent = await json(ctx, `/api/topics/${topic}/dead-letters/${consumer}`);
  return readAnswer<DeadLetterPage>(sent, `the dead letters of ${consumer}`);
}

async function waitForDeadLetter(
  ctx: CheckContext,
  topic: string,
  consumer: string,
  messageId: string,
): Promise<DeadLetter> {
  return waitFor(`${messageId} reaching ${consumer}'s dead letters`, async () => {
    const page = await readDeadLetters(ctx, topic, consumer);
    const found = page.deadLetters.find((letter) => letter.messageId === messageId);
    if (found) {
      assert.ok(page.count >= 1, `${consumer} counts ${page.count} dead letters, listing one`);
    }
    return found;
  });
}

export const deadLetterCheck: Check = {
  title:
    "a message a consumer fails on every attempt is dead-lettered with its payload and error, and can be redriven and purged",
  run: async (ctx) => {
    const payload = { probe: newProbe("doomed") };
    const messageId = await send(ctx, "doomed", payload);
    const letter = await waitForDeadLetter(ctx, "doomed", "always-fails", messageId);
    assert.equal(letter.attempts, 2);
    assert.deepEqual(letter.payload, payload);
    assert.match(letter.error, /fails every attempt/);

    const redriven = await post(ctx, "/api/topics/doomed/dead-letters/always-fails/redrive", {
      executions: [letter.execution],
    });
    assert.deepEqual(readAnswer(redriven, "the redrive"), { redriven: 1 });
    const pending = await readDeadLetters(ctx, "doomed", "always-fails");
    assert.ok(
      !pending.deadLetters.some((one) => one.execution === letter.execution),
      "a redriven message is still listed as dead",
    );
    const again = await waitForDeadLetter(ctx, "doomed", "always-fails", messageId);
    assert.equal(again.execution, letter.execution);

    const purged = await post(ctx, "/api/topics/doomed/dead-letters/always-fails/purge", {
      executions: [letter.execution],
    });
    assert.deepEqual(readAnswer(purged, "the purge"), { purged: 1 });
    const left = await readDeadLetters(ctx, "doomed", "always-fails");
    assert.ok(
      !left.deadLetters.some((one) => one.execution === letter.execution),
      "a purged dead letter is still listed",
    );
  },
};

export const consumerMaxDurationCheck: Check = {
  title:
    "a consumer attempt past its maxDuration fails, and is retried and then dead-lettered like any failure",
  run: async (ctx) => {
    const messageId = await send(ctx, "stalls", { n: 1, ms: 5_000 });
    const letter = await waitForDeadLetter(ctx, "stalls", "too-slow", messageId);
    assert.equal(letter.attempts, 2);
  },
};

const CRON_WITHIN_MS = 75_000;

export const cronCheck: Check = {
  title: "a task with a cron is triggered on its schedule",
  run: async (ctx) => {
    const run = await waitFor(
      "a heartbeat run",
      async () => {
        const [found] = await listRuns(ctx, {
          task: "heartbeat",
          status: "COMPLETED",
          limit: "1",
        });
        return found;
      },
      CRON_WITHIN_MS,
    );
    const { timestamp } = run.output as { timestamp: string };
    assert.ok(!Number.isNaN(Date.parse(timestamp)), `the cron payload is ${timestamp}`);
    assert.equal(new Date(Date.parse(timestamp)).getUTCSeconds(), 0);
  },
};

export const tasksChecks: Check[] = [
  triggeredRunCheck,
  hyphenatedTaskCheck,
  retryCheck,
  retriesExhaustedCheck,
  triggerLowersMaxAttemptsCheck,
  abortCheck,
  delayCheck,
  delayBoundCheck,
  ttlExpiryCheck,
  idempotencyCheck,
  idempotencyKeyTtlCheck,
  debounceCheck,
  orderedPerKeyCheck,
  orderedKeysInParallelCheck,
  batchCheck,
  batchRetryCheck,
  lanesCheck,
  taskConcurrencyCheck,
  workerConcurrencyCheck,
  maxDurationCheck,
  listRunsCheck,
  cancelDelayedCheck,
  cancelExecutingCheck,
  replayCheck,
  rescheduleCheck,
  fanOutCheck,
  sendIdempotencyCheck,
  deadLetterCheck,
  consumerMaxDurationCheck,
  cronCheck,
];
