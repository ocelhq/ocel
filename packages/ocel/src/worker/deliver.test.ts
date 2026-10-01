import { afterEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";

vi.mock("../runtime/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
}));

const { task, AbortTaskRunError } = await import("../task/index.js");
const { topic } = await import("../topic/index.js");
const { worker, deliver } = await import("./index.js");

type Attempt = { number: number; of: number; firstAttemptedAt?: string };

function envelope(
  topicName: string,
  consumer: string,
  payload: unknown,
  attempt: Attempt = { number: 1, of: 3 },
) {
  return JSON.stringify({
    v: 1,
    topic: topicName,
    consumer,
    execution: "01J00000000000000000000000-run",
    message: { id: "01J00000000000000000000000", publishedAt: "2026-01-02T03:04:05Z" },
    attempt: { firstAttemptedAt: "2026-01-02T03:04:06Z", ...attempt },
    payload,
  });
}

function batchEnvelope(topicName: string, consumer: string, payloads: unknown[]) {
  return JSON.stringify({
    v: 1,
    topic: topicName,
    consumer,
    messages: payloads.map((payload, i) => ({
      execution: `execution-${i}`,
      message: { id: `0000000000000000000000000${i}`, publishedAt: "2026-01-02T03:04:05Z" },
      attempt: { number: 1, of: 3 },
      payload,
    })),
  });
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("deliver", () => {
  it("runs a task with the envelope's payload and answers 200 with its output as JSON", async () => {
    const run = vi.fn(async (payload: { width: number }) => ({ resized: payload.width / 2 }));
    task("resize", { run });

    const answer = await deliver("worker", envelope("resize", "resize", { width: 640 }));

    expect(answer).toEqual({ status: 200, body: '{"resized":320}' });
    expect(run).toHaveBeenCalledWith({ width: 640 }, expect.anything());
  });

  it("answers null when the run returns nothing", async () => {
    task("noop", { run: async () => {} });

    expect(await deliver("worker", envelope("noop", "noop", 1))).toEqual({
      status: 200,
      body: "null",
    });
  });

  it("answers null when the run returns a function, which JSON leaves out", async () => {
    task("returns-function", { run: async () => () => {} });

    expect(await deliver("worker", envelope("returns-function", "returns-function", 1))).toEqual({
      status: 200,
      body: "null",
    });
  });

  it("hands the run what it knows about the attempt", async () => {
    let seen: unknown;
    task("inspect", {
      run: async (_payload, { ctx, signal }) => {
        seen = { ...ctx, signalIsCtxSignal: signal === ctx.signal };
      },
    });

    await deliver("worker", envelope("inspect", "inspect", null, { number: 2, of: 4 }));

    expect(seen).toEqual({
      kind: "task",
      name: "inspect",
      topic: "inspect",
      id: "01J00000000000000000000000-run",
      attempt: { number: 2, of: 4, firstAttemptedAt: new Date("2026-01-02T03:04:06Z") },
      message: { id: "01J00000000000000000000000", publishedAt: new Date("2026-01-02T03:04:05Z") },
      signal: expect.any(AbortSignal),
      signalIsCtxSignal: true,
    });
  });

  it("refuses a body that is not an envelope with 400", async () => {
    const answer = await deliver("worker", "{not json");

    expect(answer.status).toBe(400);
    expect(answer.body).toContain("not a message envelope");
  });

  it("answers 404 naming the topic and consumer this app never declared", async () => {
    expect(await deliver("worker", envelope("ghost", "ghost", 1))).toEqual({
      status: 404,
      body: 'no consumer "ghost" of topic "ghost" is declared in this app',
    });
  });

  it("answers 404 for a task declared on another worker, naming both workers", async () => {
    task("encode", { run: async () => {}, worker: worker("media") });

    expect(await deliver("worker", envelope("encode", "encode", 1))).toEqual({
      status: 404,
      body: 'consumer "encode" of topic "encode" runs on worker "media", not on worker "worker"',
    });
  });

  it("aborts with 422 and the abort answer when the run throws AbortTaskRunError", async () => {
    task("refuse", {
      run: async () => {
        throw new AbortTaskRunError("the image is corrupt");
      },
    });

    expect(await deliver("worker", envelope("refuse", "refuse", 1))).toEqual({
      status: 422,
      body: '{"abort":{"reason":"the image is corrupt"}}',
    });
  });

  it("asks for a retry with 500 and the error's message when the run throws", async () => {
    task("flaky", {
      run: async () => {
        throw new Error("upstream timed out");
      },
    });

    expect(await deliver("worker", envelope("flaky", "flaky", 1))).toEqual({
      status: 500,
      body: "upstream timed out",
    });
  });

  it("aborts when catchError answers skipRetrying", async () => {
    const catchError = vi.fn(() => ({ skipRetrying: true }));
    task("caught", {
      run: async () => {
        throw new Error("quota exceeded");
      },
      catchError,
    });

    const answer = await deliver("worker", envelope("caught", "caught", { n: 1 }));

    expect(answer).toEqual({ status: 422, body: '{"abort":{"reason":"quota exceeded"}}' });
    expect(catchError).toHaveBeenCalledWith({
      payload: { n: 1 },
      error: new Error("quota exceeded"),
      ctx: expect.objectContaining({ name: "caught" }),
    });
  });

  it("retries when catchError answers nothing", async () => {
    task("caught-retry", {
      run: async () => {
        throw new Error("try again");
      },
      catchError: () => {},
    });

    expect((await deliver("worker", envelope("caught-retry", "caught-retry", 1))).status).toBe(500);
  });

  it("hands the run the schema's output", async () => {
    const run = vi.fn(async (_payload: { count: number }) => {});
    task("parsed", { schema: z.object({ count: z.coerce.number() }), run });

    await deliver("worker", envelope("parsed", "parsed", { count: "7" }));

    expect(run).toHaveBeenCalledWith({ count: 7 }, expect.anything());
  });

  it("aborts a payload the schema refuses, without running", async () => {
    const run = vi.fn(async () => {});
    task("strict", { schema: z.object({ email: z.email() }), run });

    const answer = await deliver("worker", envelope("strict", "strict", { email: "nope" }));

    expect(answer.status).toBe(422);
    expect(JSON.parse(answer.body).abort.reason).toContain("email");
    expect(run).not.toHaveBeenCalled();
  });

  it("hands a batch task the list of payloads, and the first message as the context", async () => {
    let seen: { payloads: unknown; id: string } | undefined;
    task("thumbnails", {
      batch: { size: 10 },
      schema: z.object({ n: z.number() }),
      run: async (payloads, { ctx }) => {
        seen = { payloads, id: ctx.id };
      },
    });

    const answer = await deliver(
      "worker",
      batchEnvelope("thumbnails", "thumbnails", [{ n: 1 }, { n: 2 }]),
    );

    expect(answer.status).toBe(200);
    expect(seen).toEqual({ payloads: [{ n: 1 }, { n: 2 }], id: "execution-0" });
  });

  it("refuses a batch envelope for a task that takes one message with 400", async () => {
    task("single", { run: async () => {} });

    expect((await deliver("worker", batchEnvelope("single", "single", [1, 2]))).status).toBe(400);
  });

  it("wraps the run in the worker's middleware, onStartAttempt, then the task's middleware", async () => {
    const order: string[] = [];
    const layered = worker("layered", {
      middleware: async ({ ctx, next }) => {
        order.push(`worker ${ctx.kind} ${ctx.name}`);
        await next();
        order.push("worker after");
      },
    });
    task("wrapped", {
      worker: layered,
      onStartAttempt: () => {
        order.push("onStartAttempt");
      },
      middleware: async ({ next }) => {
        order.push("task middleware");
        await next();
      },
      run: async () => {
        order.push("run");
      },
    });

    await deliver("layered", envelope("wrapped", "wrapped", 1));

    expect(order).toEqual([
      "worker task wrapped",
      "onStartAttempt",
      "task middleware",
      "run",
      "worker after",
    ]);
  });

  it("fails the attempt when a middleware throws", async () => {
    const run = vi.fn(async () => {});
    task("guarded", {
      middleware: () => {
        throw new Error("not allowed");
      },
      run,
    });

    expect(await deliver("worker", envelope("guarded", "guarded", 1))).toEqual({
      status: 500,
      body: "not allowed",
    });
    expect(run).not.toHaveBeenCalled();
  });
});

describe("deliver's task lifecycle hooks", () => {
  it("calls onSuccess then onComplete with the output when the run succeeds", async () => {
    const calls: unknown[] = [];
    task("hooked-ok", {
      run: async (payload: number) => payload * 2,
      onSuccess: ({ payload, output }) => {
        calls.push(["onSuccess", payload, output]);
      },
      onComplete: ({ result }) => {
        calls.push(["onComplete", result]);
      },
      onFailure: () => {
        calls.push(["onFailure"]);
      },
    });

    await deliver("worker", envelope("hooked-ok", "hooked-ok", 21));

    expect(calls).toEqual([
      ["onSuccess", 21, 42],
      ["onComplete", { ok: true, output: 42 }],
    ]);
  });

  it("calls no lifecycle hook when a failed attempt will be retried", async () => {
    const onFailure = vi.fn();
    const onComplete = vi.fn();
    task("hooked-retry", {
      run: async () => {
        throw new Error("again");
      },
      onFailure,
      onComplete,
    });

    await deliver("worker", envelope("hooked-retry", "hooked-retry", 1, { number: 1, of: 3 }));

    expect(onFailure).not.toHaveBeenCalled();
    expect(onComplete).not.toHaveBeenCalled();
  });

  it("calls onFailure then onComplete when the last attempt fails", async () => {
    const calls: unknown[] = [];
    task("hooked-last", {
      run: async () => {
        throw new Error("gone");
      },
      onFailure: ({ error }) => {
        calls.push(["onFailure", (error as Error).message]);
      },
      onComplete: ({ result }) => {
        calls.push(["onComplete", result.ok]);
      },
    });

    const answer = await deliver(
      "worker",
      envelope("hooked-last", "hooked-last", 1, { number: 3, of: 3 }),
    );

    expect(answer.status).toBe(500);
    expect(calls).toEqual([
      ["onFailure", "gone"],
      ["onComplete", false],
    ]);
  });

  it("calls onFailure when the run aborts on an early attempt", async () => {
    const onFailure = vi.fn();
    task("hooked-abort", {
      run: async () => {
        throw new AbortTaskRunError("no");
      },
      onFailure,
    });

    await deliver("worker", envelope("hooked-abort", "hooked-abort", 1, { number: 1, of: 3 }));

    expect(onFailure).toHaveBeenCalledOnce();
  });

  it("reports a throwing lifecycle hook to stderr without changing the answer", async () => {
    const stderr = vi.spyOn(console, "error").mockImplementation(() => {});
    task("hooked-throws", {
      run: async () => "done",
      onSuccess: () => {
        throw new Error("hook broke");
      },
    });

    const answer = await deliver("worker", envelope("hooked-throws", "hooked-throws", 1));

    expect(answer).toEqual({ status: 200, body: '"done"' });
    expect(stderr).toHaveBeenCalledWith(
      'ocel: task "hooked-throws" onSuccess threw',
      new Error("hook broke"),
    );
  });

  it("calls onCancel when the run is canceled during the attempt, and no completion hook", async () => {
    const controller = new AbortController();
    const onCancel = vi.fn();
    const onComplete = vi.fn();
    task("hooked-cancel", {
      run: async (_payload, { signal }) => {
        controller.abort();
        signal.throwIfAborted();
      },
      onCancel,
      onComplete,
    });

    await deliver(
      "worker",
      envelope("hooked-cancel", "hooked-cancel", 5, { number: 3, of: 3 }),
      controller.signal,
    );

    expect(onCancel).toHaveBeenCalledWith({
      payload: 5,
      ctx: expect.objectContaining({ name: "hooked-cancel" }),
    });
    expect(onComplete).not.toHaveBeenCalled();
  });

  it("answers 200 with the output and calls onCancel but neither onSuccess nor onComplete when a canceled run succeeds", async () => {
    const controller = new AbortController();
    const onCancel = vi.fn();
    const onSuccess = vi.fn();
    const onComplete = vi.fn();
    task("hooked-cancel-succeeds", {
      run: async (payload: number) => {
        controller.abort();
        return payload + 1;
      },
      onCancel,
      onSuccess,
      onComplete,
    });

    const answer = await deliver(
      "worker",
      envelope("hooked-cancel-succeeds", "hooked-cancel-succeeds", 1),
      controller.signal,
    );

    expect(answer).toEqual({ status: 200, body: "2" });
    expect(onCancel).toHaveBeenCalledOnce();
    expect(onSuccess).not.toHaveBeenCalled();
    expect(onComplete).not.toHaveBeenCalled();
  });

  it.each([
    ["a BigInt", "unencodable-bigint", () => ({ count: 1n })],
    [
      "a cycle",
      "unencodable-cycle",
      () => {
        const cycle: Record<string, unknown> = {};
        cycle.self = cycle;
        return cycle;
      },
    ],
  ])(
    "aborts with 422 and calls onFailure then onComplete when the output holds %s, which JSON cannot encode",
    async (_label, name, run) => {
      const calls: unknown[] = [];
      const onSuccess = vi.fn();
      task(name, {
        run,
        onSuccess,
        onFailure: ({ error }) => {
          calls.push(["onFailure", (error as Error).message]);
        },
        onComplete: ({ result }) => {
          calls.push(["onComplete", result.ok]);
        },
      });

      const answer = await deliver("worker", envelope(name, name, 1, { number: 1, of: 3 }));

      expect(answer.status).toBe(422);
      const { abort } = JSON.parse(answer.body) as { abort: { reason: string } };
      expect(abort.reason).toMatch(/^the output does not encode as JSON: /);
      expect(onSuccess).not.toHaveBeenCalled();
      expect(calls).toEqual([
        ["onFailure", abort.reason],
        ["onComplete", false],
      ]);
    },
  );
});

describe("deliver's worker", () => {
  it("runs onStart once before the first delivery, even when deliveries arrive together", async () => {
    let starts = 0;
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const booting = worker("booting", {
      onStart: async () => {
        starts++;
        await gate;
      },
    });
    const run = vi.fn(async () => {});
    task("after-boot", { worker: booting, run });

    const first = deliver("booting", envelope("after-boot", "after-boot", 1));
    const second = deliver("booting", envelope("after-boot", "after-boot", 2));
    await Promise.resolve();
    expect(run).not.toHaveBeenCalled();
    release();

    expect((await first).status).toBe(200);
    expect((await second).status).toBe(200);
    expect(starts).toBe(1);
  });

  it("fails every delivery with 500 while onStart fails, and tries it again on the next", async () => {
    let attempts = 0;
    const fragile = worker("fragile", {
      onStart: () => {
        attempts++;
        if (attempts === 1) throw new Error("database down");
      },
    });
    task("after-fragile", { worker: fragile, run: async () => {} });

    expect(await deliver("fragile", envelope("after-fragile", "after-fragile", 1))).toEqual({
      status: 500,
      body: 'worker "fragile" failed to start: database down',
    });
    expect((await deliver("fragile", envelope("after-fragile", "after-fragile", 1))).status).toBe(
      200,
    );
    expect(attempts).toBe(2);
  });

  it("serves no more runs at once than the worker's concurrency, across tasks and consumers", async () => {
    const narrow = worker("narrow", { concurrency: 2 });
    let inFlight = 0;
    let most = 0;
    const work = async () => {
      inFlight++;
      most = Math.max(most, inFlight);
      await new Promise((resolve) => setTimeout(resolve, 5));
      inFlight--;
    };
    task("narrow-task", { worker: narrow, run: work });
    topic("narrow-topic").consumer("narrow-consumer", work, { worker: narrow });

    await Promise.all([
      deliver("narrow", envelope("narrow-task", "narrow-task", 1)),
      deliver("narrow", envelope("narrow-task", "narrow-task", 2)),
      deliver("narrow", envelope("narrow-topic", "narrow-consumer", 3)),
      deliver("narrow", envelope("narrow-topic", "narrow-consumer", 4)),
    ]);

    expect(most).toBe(2);
  });

  it("answers 500 without running a delivery aborted while it waits for a slot, and leaves the slot to the next delivery", async () => {
    const single = worker("single", { concurrency: 1 });
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const payloads: number[] = [];
    task("single-task", {
      worker: single,
      run: async (payload: number) => {
        payloads.push(payload);
        if (payload === 1) await gate;
      },
    });

    const holding = deliver("single", envelope("single-task", "single-task", 1));
    const controller = new AbortController();
    const waiting = deliver("single", envelope("single-task", "single-task", 2), controller.signal);
    await new Promise((resolve) => setTimeout(resolve, 5));
    controller.abort();

    expect((await waiting).status).toBe(500);
    release();
    expect((await holding).status).toBe(200);
    expect((await deliver("single", envelope("single-task", "single-task", 3))).status).toBe(200);
    expect(payloads).toEqual([1, 3]);
  });
});

describe("deliver to a topic consumer", () => {
  it("runs the consumer named in the envelope with a consumer context", async () => {
    let seen: unknown;
    topic<{ orderId: string }>("orders").consumer("bill", async (payload, { ctx }) => {
      seen = [payload, ctx.kind, ctx.name, ctx.topic];
    });

    expect(await deliver("worker", envelope("orders", "bill", { orderId: "o1" }))).toEqual({
      status: 200,
      body: "null",
    });
    expect(seen).toEqual([{ orderId: "o1" }, "consumer", "bill", "orders"]);
  });

  it("hands a batch consumer the list of payloads", async () => {
    let seen: unknown;
    topic("clicks").batchConsumer(
      "count-clicks",
      async (payloads) => {
        seen = payloads;
      },
      { batchSize: 100 },
    );

    await deliver("worker", batchEnvelope("clicks", "count-clicks", [1, 2, 3]));

    expect(seen).toEqual([1, 2, 3]);
  });

  it("aborts a consumer that throws AbortTaskRunError", async () => {
    topic("payments").consumer("refund", async () => {
      throw new AbortTaskRunError("already refunded");
    });

    expect(await deliver("worker", envelope("payments", "refund", 1))).toEqual({
      status: 422,
      body: '{"abort":{"reason":"already refunded"}}',
    });
  });

  it("validates a consumer's payload against the topic's schema", async () => {
    topic("signups", { schema: z.object({ email: z.email() }) }).consumer(
      "welcome",
      async () => {},
    );

    expect((await deliver("worker", envelope("signups", "welcome", { email: "x" }))).status).toBe(
      422,
    );
  });
});
