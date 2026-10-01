import { fromJson, type JsonValue } from "@bufbuild/protobuf";
import { type Timestamp, timestampDate } from "@bufbuild/protobuf/wkt";
import { validatePayload } from "../delivery/schema.js";
import { type Envelope, EnvelopeSchema } from "../gen/proto/app/topic/v1/topic_pb.js";
import { AbortTaskRunError } from "../task/errors.js";
import type { CatchErrorResult, TaskResult } from "../task/hooks.js";
import type { RunContext } from "./context.js";
import { findRegistration, findWorker, type Registration } from "./registry.js";
import { Semaphore } from "./semaphore.js";

/** The HTTP answer `deliver` gives the message's sender. */
export interface DeliveryAnswer {
  /** 200 on success, 422 to fail without retry, 500 to retry, 400 or 404 for a bad delivery. */
  status: number;
  /** The run's output as JSON, the abort as JSON, or a plain-text reason. */
  body: string;
}

const describeError = (error: unknown) => (error instanceof Error ? error.message : String(error));

const createAbortAnswer = (reason: string): DeliveryAnswer => ({
  status: 422,
  body: JSON.stringify({ abort: { reason } }),
});

const decodeTimestamp = (timestamp: Timestamp | undefined) =>
  timestamp ? timestampDate(timestamp) : undefined;

interface ParsedEnvelope {
  envelope: Envelope;
  payloads: unknown[];
}

function parseEnvelope(body: string): ParsedEnvelope {
  const parsed: unknown = JSON.parse(body);
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    throw new Error("the envelope is not a JSON object");
  }
  const { payload, messages, ...fields } = parsed as Record<string, unknown>;
  const deliveries = Array.isArray(messages) ? (messages as Record<string, unknown>[]) : [];
  const bare = Array.isArray(messages)
    ? deliveries.map(({ payload: _payload, ...delivery }) => delivery)
    : messages;
  const envelope = fromJson(
    EnvelopeSchema,
    (bare === undefined ? fields : { ...fields, messages: bare }) as JsonValue,
    { ignoreUnknownFields: true },
  );
  const payloads =
    deliveries.length > 0
      ? deliveries.map((delivery) => delivery.payload ?? null)
      : [payload ?? null];
  return { envelope, payloads };
}

const starts = new Map<string, Promise<unknown>>();
const semaphores = new Map<string, Semaphore>();

function ensureStarted(workerName: string): Promise<unknown> {
  const onStart = findWorker(workerName)?.onStart;
  if (!onStart) return Promise.resolve();
  let start = starts.get(workerName);
  if (!start) {
    start = Promise.resolve()
      .then(onStart)
      .catch((error: unknown) => {
        starts.delete(workerName);
        throw error;
      });
    starts.set(workerName, start);
  }
  return start;
}

function ensureSemaphore(workerName: string): Semaphore | undefined {
  const concurrency = findWorker(workerName)?.concurrency;
  if (!concurrency || concurrency < 1) return undefined;
  let semaphore = semaphores.get(workerName);
  if (!semaphore) {
    semaphore = new Semaphore(concurrency);
    semaphores.set(workerName, semaphore);
  }
  return semaphore;
}

function reportHookError(hook: string, registration: Registration, error: unknown): void {
  console.error(`ocel: ${registration.kind} "${registration.name}" ${hook} threw`, error);
}

async function callHook(
  hook: string,
  registration: Registration,
  call: () => unknown,
): Promise<void> {
  try {
    await call();
  } catch (error) {
    reportHookError(hook, registration, error);
  }
}

function createRunContext(
  registration: Registration,
  envelope: Envelope,
  signal: AbortSignal,
): RunContext {
  const first = envelope.messages[0];
  const message = first?.message ?? envelope.message;
  const attempt = first?.attempt ?? envelope.attempt;
  return {
    kind: registration.kind,
    name: registration.name,
    topic: registration.topic,
    id: first?.execution ?? envelope.execution,
    attempt: {
      number: attempt?.number ?? 1,
      of: attempt?.of ?? 1,
      firstAttemptedAt: decodeTimestamp(attempt?.firstAttemptedAt),
    },
    message: { id: message?.id ?? "", publishedAt: decodeTimestamp(message?.publishedAt) },
    signal,
  };
}

async function decodePayload(
  registration: Registration,
  { envelope, payloads: values }: ParsedEnvelope,
): Promise<{ ok: true; payload: unknown } | { ok: false; answer: DeliveryAnswer }> {
  const isBatchEnvelope = envelope.messages.length > 0;
  if (!registration.batch && isBatchEnvelope) {
    return {
      ok: false,
      answer: {
        status: 400,
        body: `${registration.kind} "${registration.name}" takes one message at a time, and this envelope carries a batch`,
      },
    };
  }
  const { schema } = registration;
  if (!schema) return { ok: true, payload: registration.batch ? values : values[0] };
  const validated: unknown[] = [];
  for (const value of values) {
    const result = await validatePayload(schema, value);
    if (!result.ok) {
      return {
        ok: false,
        answer: createAbortAnswer(`the payload does not match the schema: ${result.message}`),
      };
    }
    validated.push(result.value);
  }
  return { ok: true, payload: registration.batch ? validated : validated[0] };
}

async function serveAttempt(
  registration: Registration,
  workerName: string,
  delivered: ParsedEnvelope,
  signal: AbortSignal,
): Promise<DeliveryAnswer> {
  const parsed = await decodePayload(registration, delivered);
  if (!parsed.ok) return parsed.answer;
  const { payload } = parsed;
  const ctx = createRunContext(registration, delivered.envelope, signal);
  const { hooks } = registration;
  const workerMiddleware = findWorker(workerName)?.middleware;

  let canceled = false;
  const onAbort = () => {
    canceled = true;
    if (hooks.onCancel) {
      void callHook("onCancel", registration, () => hooks.onCancel?.({ payload, ctx }));
    }
  };
  if (signal.aborted) onAbort();
  else signal.addEventListener("abort", onAbort, { once: true });

  let output: unknown;
  const runAttempt = async () => {
    await hooks.onStartAttempt?.({ payload, ctx });
    const run = async () => {
      output = await registration.run(payload, { ctx, signal });
    };
    if (hooks.middleware) await hooks.middleware({ payload, ctx, next: run });
    else await run();
  };

  let failure: { error: unknown } | undefined;
  try {
    if (workerMiddleware) await workerMiddleware({ ctx, next: runAttempt });
    else await runAttempt();
  } catch (error) {
    failure = { error };
  } finally {
    signal.removeEventListener("abort", onAbort);
  }

  let body = "null";
  if (!failure) {
    try {
      body = JSON.stringify(output ?? null) ?? "null";
    } catch (error) {
      failure = {
        error: new AbortTaskRunError(`the output does not encode as JSON: ${describeError(error)}`),
      };
    }
  }

  if (!failure) {
    if (!canceled)
      await callCompletionHooks(registration, { payload, ctx, result: { ok: true, output } });
    return { status: 200, body };
  }

  const { error } = failure;
  let abort = error instanceof AbortTaskRunError;
  if (!abort && hooks.catchError) {
    try {
      const answer = (await hooks.catchError({ payload, error, ctx })) as
        | CatchErrorResult
        | undefined;
      abort = answer?.skipRetrying === true;
    } catch (hookError) {
      reportHookError("catchError", registration, hookError);
    }
  }
  if (!canceled && (abort || ctx.attempt.number >= ctx.attempt.of)) {
    await callCompletionHooks(registration, { payload, ctx, result: { ok: false, error } });
  }
  return abort
    ? createAbortAnswer(describeError(error))
    : { status: 500, body: describeError(error) };
}

async function callCompletionHooks(
  registration: Registration,
  args: { payload: unknown; ctx: RunContext; result: TaskResult<unknown> },
): Promise<void> {
  const { hooks } = registration;
  const { payload, ctx, result } = args;
  if (result.ok) {
    if (hooks.onSuccess) {
      await callHook("onSuccess", registration, () =>
        hooks.onSuccess?.({ payload, output: result.output, ctx }),
      );
    }
  } else if (hooks.onFailure) {
    await callHook("onFailure", registration, () =>
      hooks.onFailure?.({ payload, error: result.error, ctx }),
    );
  }
  if (hooks.onComplete) {
    await callHook("onComplete", registration, () => hooks.onComplete?.({ payload, ctx, result }));
  }
}

/**
 * Serves one message the worker named `workerName` received: `body` is the envelope POSTed
 * to it, and `signal` aborts when the run is canceled. Answers what to send back: 200 with
 * the output, 422 to fail without retry, 500 to retry, 400 or 404 for a bad delivery.
 */
export async function deliver(
  workerName: string,
  body: string,
  signal: AbortSignal = new AbortController().signal,
): Promise<DeliveryAnswer> {
  let delivered: ParsedEnvelope;
  try {
    delivered = parseEnvelope(body);
  } catch (error) {
    return { status: 400, body: `the body is not a message envelope: ${describeError(error)}` };
  }
  const { envelope } = delivered;
  const registration = findRegistration(envelope.topic, envelope.consumer);
  if (!registration) {
    return {
      status: 404,
      body: `no consumer "${envelope.consumer}" of topic "${envelope.topic}" is declared in this app`,
    };
  }
  if (registration.worker !== workerName) {
    return {
      status: 404,
      body: `consumer "${envelope.consumer}" of topic "${envelope.topic}" runs on worker "${registration.worker}", not on worker "${workerName}"`,
    };
  }
  try {
    await ensureStarted(workerName);
  } catch (error) {
    return { status: 500, body: `worker "${workerName}" failed to start: ${describeError(error)}` };
  }
  const semaphore = ensureSemaphore(workerName);
  if (!semaphore) return serveAttempt(registration, workerName, delivered, signal);
  try {
    await semaphore.acquire(signal);
  } catch {
    return {
      status: 500,
      body: `the delivery was canceled while it waited for a free slot on worker "${workerName}"`,
    };
  }
  try {
    return await serveAttempt(registration, workerName, delivered, signal);
  } finally {
    semaphore.release();
  }
}
