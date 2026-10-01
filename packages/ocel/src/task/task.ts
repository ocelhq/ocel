import type { JsonObject } from "@bufbuild/protobuf";
import { type Client, createClient } from "@connectrpc/connect";
import type { StandardSchemaV1 } from "@standard-schema/spec";
import { type Duration, encodeDueAt, encodeDuration } from "../delivery/duration.js";
import { encodeLane, type Lane } from "../delivery/lane.js";
import { encodePayload } from "../delivery/payload.js";
import { encodeRetryPolicy, type RetryOptions } from "../delivery/retry.js";
import { encodeJsonSchema } from "../delivery/schema.js";
import { ResourceType } from "../gen/proto/app/resources/v1/resources_pb.js";
import { TaskService } from "../gen/proto/app/task/v1/task_pb.js";
import { createRuntimeTransport } from "../runtime/transport.js";
import { declarationSite } from "../utils/callsite.js";
import { defer } from "../utils/defer.js";
import { getConfig } from "../utils/get-config.js";
import { unprovisioned, unprovisionedPhase } from "../utils/phase.js";
import { rpc } from "../utils/rpc.js";
import type { RunOptions } from "../worker/context.js";
import { DEFAULT_WORKER, registerRun } from "../worker/registry.js";
import type { Worker } from "../worker/worker.js";
import type { TaskHooks } from "./hooks.js";

/** How many runs a task takes at once, and how long it waits to fill them. */
export interface BatchOptions {
  /** The most runs one attempt receives. */
  size: number;
  /** How long a batch waits to fill before its runs are delivered. */
  timeout?: Duration;
}

/** What a task's `run` receives for a payload, and returns. */
export type RunFunction<TPayload, TOutput> = (
  payload: TPayload,
  options: RunOptions,
) => TOutput | Promise<TOutput>;

/** How a task is declared. */
export interface TaskOptions<TPayload, TOutput> extends TaskHooks<TPayload, TOutput> {
  /** A Standard Schema every payload is validated against before `run` sees it. */
  schema?: StandardSchemaV1;
  /** Does the work of one run, or of one batch when `batch` is set. */
  run: RunFunction<TPayload, TOutput>;
  /** How a failed attempt is retried. */
  retry?: RetryOptions;
  /** The most runs of this task in progress at once. */
  concurrency?: number;
  /** The longest an attempt may take before the run ends as timed out. */
  maxDuration?: Duration;
  /** How long a run may wait to start before it expires. */
  ttl?: Duration;
  /** Runs one at a time per trigger `key`, in the order they were triggered. */
  ordered?: boolean;
  /** Delivers runs in batches; `run` then receives a list of payloads. */
  batch?: BatchOptions;
  /** The worker that serves this task; without one, the worker named `worker`. */
  worker?: Worker;
  /** A cron expression this task is triggered on. */
  cron?: string;
}

/** How one trigger is run. */
export interface TriggerOptions {
  /** How long to wait before the run is due, or the date it is due at. */
  delay?: Duration | Date;
  /** How long the run may wait to start before it expires. */
  ttl?: Duration;
  /** A trigger repeating a key still remembered returns the run it started instead of a new one. */
  idempotencyKey?: string;
  /** How long `idempotencyKey` is remembered. */
  idempotencyKeyTTL?: Duration;
  /** Triggers sharing `key` within `delay` of each other start one run, with the last payload. */
  debounce?: { key: string; delay: Duration };
  /** Runs of an `ordered` task sharing this key run one at a time, in order. */
  key?: string;
  /** The lane the run waits in. */
  lane?: Lane;
  /** Lowers the task's own `retry.maxAttempts` for this run. */
  maxAttempts?: number;
  /** Tags `runs.list` can filter by. */
  tags?: string[];
  /** Values stored with the run. */
  metadata?: JsonObject;
}

/** One item of a `batchTrigger`. */
export interface BatchTriggerItem<TInput> {
  /** The run's payload. */
  payload: TInput;
  /** How the run is triggered. */
  options?: TriggerOptions;
}

/** A triggered run. */
export interface RunHandle {
  /** The run's id, which `runs` reads and acts on. */
  id: string;
}

/** The binding fields a task resolves at runtime. */
export interface ResolvedTaskConfig {
  /** The name the task is bound under. */
  task: string;
}

function encodeTriggerOptions(options: TriggerOptions = {}) {
  return {
    dueAt: encodeDueAt(options.delay),
    ttl: encodeDuration(options.ttl),
    idempotencyKey: options.idempotencyKey ?? "",
    idempotencyKeyTtl: encodeDuration(options.idempotencyKeyTTL),
    debounce: options.debounce && {
      key: options.debounce.key,
      delay: encodeDuration(options.debounce.delay),
    },
    key: options.key ?? "",
    lane: encodeLane(options.lane),
    maxAttempts: options.maxAttempts ?? 0,
    tags: options.tags ?? [],
    metadata: options.metadata,
  };
}

/** A declared task, and the handle its runs are triggered through. */
export class Task<TInput = unknown, TOutput = unknown> {
  /** The name this task was declared under. */
  readonly name: string;
  /** Type-only: the payload `trigger` takes and the output `run` returns. */
  declare readonly types?: { input: TInput; output: TOutput };
  private client: Client<typeof TaskService> | undefined;

  constructor(name: string, options: TaskOptions<never, unknown>) {
    this.name = name;
    registerRun({
      kind: "task",
      topic: name,
      name,
      worker: options.worker?.name ?? DEFAULT_WORKER,
      batch: options.batch !== undefined,
      schema: options.schema,
      run: options.run as RunFunction<unknown, unknown>,
      hooks: options as TaskHooks<unknown, unknown>,
    });
    if (process.env.OCEL_PHASE === "discovery") {
      defer(
        rpc.resource.declare({
          resource: { name, type: ResourceType.TASK },
          config: {
            case: "task",
            value: {
              schema: encodeJsonSchema(options.schema),
              ordered: options.ordered ?? false,
              retry: encodeRetryPolicy(options.retry),
              concurrency: options.concurrency ?? 0,
              maxDuration: encodeDuration(options.maxDuration),
              ttl: encodeDuration(options.ttl),
              batch: options.batch && {
                size: options.batch.size,
                timeout: encodeDuration(options.batch.timeout),
              },
              worker: options.worker?.name ?? "",
              cron: options.cron ?? "",
            },
          },
          source: declarationSite(),
        }),
      );
    }
  }

  /** The binding delivered for this task. */
  __config(): ResolvedTaskConfig {
    if (unprovisionedPhase()) {
      throw unprovisioned(`task("${this.name}")`, "__config");
    }
    return getConfig(this.name, "task");
  }

  private ensureClient(operation: string): Client<typeof TaskService> {
    if (unprovisionedPhase()) {
      throw unprovisioned(`task("${this.name}")`, operation);
    }
    return (this.client ??= createClient(TaskService, createRuntimeTransport()));
  }

  /** Starts a run of this task with `payload`. */
  async trigger(payload: TInput, options?: TriggerOptions): Promise<RunHandle> {
    const client = this.ensureClient("trigger");
    const { id } = await client.trigger({
      task: this.__config().task,
      payload: encodePayload(payload),
      options: encodeTriggerOptions(options),
    });
    return { id };
  }

  /** Starts one run per item, and answers their handles in the order of `items`. */
  async batchTrigger(items: BatchTriggerItem<TInput>[]): Promise<RunHandle[]> {
    const client = this.ensureClient("batchTrigger");
    const { ids } = await client.batchTrigger({
      task: this.__config().task,
      items: items.map((item) => ({
        payload: encodePayload(item.payload),
        options: encodeTriggerOptions(item.options),
      })),
    });
    return ids.map((id) => ({ id }));
  }
}

type SchemaOptions<TSchema extends StandardSchemaV1, TPayload, TOutput> = Omit<
  TaskOptions<TPayload, TOutput>,
  "schema"
> & { schema: TSchema };

/** Declares a task whose payloads `schema` validates, delivered in batches. */
export function task<TSchema extends StandardSchemaV1, TOutput = undefined>(
  name: string,
  options: SchemaOptions<TSchema, StandardSchemaV1.InferOutput<TSchema>[], TOutput> & {
    batch: BatchOptions;
  },
): Task<StandardSchemaV1.InferInput<TSchema>, TOutput>;
/** Declares a task whose payloads `schema` validates. */
export function task<TSchema extends StandardSchemaV1, TOutput = undefined>(
  name: string,
  options: SchemaOptions<TSchema, StandardSchemaV1.InferOutput<TSchema>, TOutput> & {
    batch?: undefined;
  },
): Task<StandardSchemaV1.InferInput<TSchema>, TOutput>;
/** Declares a task delivered in batches, typed by the list its `run` takes. */
export function task<TPayload = unknown, TOutput = undefined>(
  name: string,
  options: TaskOptions<TPayload[], TOutput> & { schema?: undefined; batch: BatchOptions },
): Task<TPayload, TOutput>;
/** Declares a task, typed by the payload its `run` takes. */
export function task<TPayload = unknown, TOutput = undefined>(
  name: string,
  options: TaskOptions<TPayload, TOutput> & { schema?: undefined; batch?: undefined },
): Task<TPayload, TOutput>;
export function task(name: string, options: TaskOptions<never, unknown>): Task {
  return new Task(name, options);
}
