import { type Client, createClient } from "@connectrpc/connect";
import type { StandardSchemaV1 } from "@standard-schema/spec";
import { refuseUnbound } from "../binding/binding.js";
import { unprovisioned, unprovisionedPhase } from "../binding/unprovisioned.js";
import { declarationSite } from "../declaration/callsite.js";
import { defer } from "../declaration/defer.js";
import { type Duration, encodeDueAt, encodeDuration } from "../delivery/duration.js";
import { encodeLane, type Lane } from "../delivery/lane.js";
import { encodePayload } from "../delivery/payload.js";
import { encodeRetryPolicy, type RetryOptions } from "../delivery/retry.js";
import { encodeJsonSchema } from "../delivery/schema.js";
import { ResourceType } from "../gen/proto/app/resources/v1/resources_pb.js";
import { TopicService } from "../gen/proto/app/topic/v1/topic_pb.js";
import { rpc } from "../runtime/rpc.js";
import { createRuntimeTransport } from "../runtime/transport.js";
import type { RunOptions } from "../worker/context.js";
import { DEFAULT_WORKER, registerRun } from "../worker/registry.js";
import type { Worker } from "../worker/worker.js";
import { DeadLetters } from "./dead-letters.js";

/** How a topic is declared. */
export interface TopicOptions<TSchema extends StandardSchemaV1 | undefined> {
  /** A Standard Schema every consumer validates a payload against before it sees it. */
  schema?: TSchema;
  /** Delivers messages sharing a send `key` one at a time, in the order they were sent. */
  ordered?: boolean;
  /** How a consumer's failed attempt is retried, unless the consumer says otherwise. */
  retry?: RetryOptions;
}

/** How one message is sent. */
export interface SendOptions {
  /** How long to wait before the message is delivered, or the date it is delivered at. */
  delay?: Duration | Date;
  /** A send repeating a key still remembered is delivered once. */
  idempotencyKey?: string;
  /** Messages of an `ordered` topic sharing this key are delivered one at a time, in order. */
  key?: string;
  /** The lane the message waits in. */
  lane?: Lane;
}

/** How a consumer is declared. */
export interface ConsumerOptions {
  /** How a failed attempt is retried; without it, the topic's `retry`. */
  retry?: RetryOptions;
  /** The most messages this consumer serves at once. */
  concurrency?: number;
  /** The lanes this consumer reads; without them, every lane. */
  lanes?: Lane[];
  /** The longest an attempt may take before it counts as failed. */
  maxDuration?: Duration;
  /** The worker that serves this consumer; without one, the worker named `worker`. */
  worker?: Worker;
}

/** How a batch consumer is declared. */
export interface BatchConsumerOptions extends ConsumerOptions {
  /** The most messages one attempt receives. */
  batchSize: number;
  /** How long a batch waits to fill before it is delivered. */
  batchTimeout?: Duration;
}

/** What a consumer does with one message. */
export type ConsumerFunction<TPayload> = (payload: TPayload, options: RunOptions) => unknown;

/** A declared consumer of a topic. */
export interface Consumer {
  /** The name this consumer was declared under. */
  readonly name: string;
  /** The name of the topic it consumes. */
  readonly topic: string;
}

/** A declared topic, and the handle its messages are sent through. */
export class Topic<TInput = unknown, TOutput = TInput> {
  /** The name this topic was declared under. */
  readonly name: string;
  /** Type-only: the payload `send` takes and the payload a consumer receives. */
  declare readonly types?: { input: TInput; output: TOutput };
  private readonly schema: StandardSchemaV1 | undefined;
  private client: Client<typeof TopicService> | undefined;

  constructor(name: string, options: TopicOptions<StandardSchemaV1 | undefined>) {
    this.name = name;
    this.schema = options.schema;
    if (process.env.OCEL_PHASE === "discovery") {
      defer(
        rpc.resource.declare({
          resource: { name, type: ResourceType.TOPIC },
          config: {
            case: "topic",
            value: {
              schema: encodeJsonSchema(options.schema),
              ordered: options.ordered ?? false,
              retry: encodeRetryPolicy(options.retry),
            },
          },
          source: declarationSite(),
        }),
      );
    }
  }

  private ensureClient(operation: string): Client<typeof TopicService> {
    if (unprovisionedPhase()) {
      throw unprovisioned(`topic("${this.name}")`, operation);
    }
    const refusal = refuseUnbound(this.name, "topic");
    if (refusal) {
      throw refusal;
    }
    return (this.client ??= createClient(TopicService, createRuntimeTransport()));
  }

  /** Sends `payload` to every consumer of this topic, and answers the message's id. */
  async send(payload: TInput, options: SendOptions = {}): Promise<string> {
    const client = this.ensureClient("send");
    const { messageId } = await client.send({
      topic: this.name,
      payload: encodePayload(payload),
      dueAt: encodeDueAt(options.delay),
      idempotencyKey: options.idempotencyKey ?? "",
      key: options.key ?? "",
      lane: encodeLane(options.lane),
    });
    return messageId;
  }

  private declareConsumer(
    name: string,
    run: (payload: unknown, options: RunOptions) => unknown,
    options: ConsumerOptions,
    batch: { size: number; timeout?: Duration } | undefined,
  ): Consumer {
    registerRun({
      kind: "consumer",
      topic: this.name,
      name,
      worker: options.worker?.name ?? DEFAULT_WORKER,
      batch: batch !== undefined,
      schema: this.schema,
      run,
      hooks: {},
    });
    if (process.env.OCEL_PHASE === "discovery") {
      defer(
        rpc.resource.declare({
          resource: { name, type: ResourceType.CONSUMER },
          config: {
            case: "consumer",
            value: {
              topic: this.name,
              worker: options.worker?.name ?? "",
              retry: encodeRetryPolicy(options.retry),
              concurrency: options.concurrency ?? 0,
              maxDuration: encodeDuration(options.maxDuration),
              lanes: (options.lanes ?? []).map(encodeLane),
              batch: batch && { size: batch.size, timeout: encodeDuration(batch.timeout) },
            },
          },
          source: declarationSite(),
        }),
      );
    }
    return { name, topic: this.name };
  }

  /** Declares a consumer that receives every message sent to this topic, one at a time. */
  consumer(name: string, run: ConsumerFunction<TOutput>, options: ConsumerOptions = {}): Consumer {
    return this.declareConsumer(name, run as ConsumerFunction<unknown>, options, undefined);
  }

  /** Declares a consumer that receives the messages sent to this topic in batches. */
  batchConsumer(
    name: string,
    run: ConsumerFunction<TOutput[]>,
    options: BatchConsumerOptions,
  ): Consumer {
    const { batchSize, batchTimeout, ...rest } = options;
    return this.declareConsumer(name, run as ConsumerFunction<unknown>, rest, {
      size: batchSize,
      timeout: batchTimeout,
    });
  }

  /** The messages a consumer of this topic gave up on. */
  deadLetter(consumer: string | Consumer): DeadLetters {
    return new DeadLetters(
      typeof consumer === "string" ? consumer : consumer.name,
      (operation) => ({
        client: this.ensureClient(operation),
        topic: this.name,
      }),
    );
  }
}

/** Declares a topic whose payloads `schema` validates. */
export function topic<TSchema extends StandardSchemaV1>(
  name: string,
  options: TopicOptions<TSchema> & { schema: TSchema },
): Topic<StandardSchemaV1.InferInput<TSchema>, StandardSchemaV1.InferOutput<TSchema>>;
/** Declares a topic whose payloads are of type `TPayload`. */
export function topic<TPayload = unknown>(
  name: string,
  options?: TopicOptions<undefined>,
): Topic<TPayload, TPayload>;
export function topic(
  name: string,
  options: TopicOptions<StandardSchemaV1 | undefined> = {},
): Topic {
  return new Topic(name, options);
}
