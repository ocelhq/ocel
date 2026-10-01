import { randomBytes } from "node:crypto";
import type { StandardSchemaV1 } from "@standard-schema/spec";
import { getConfig } from "../binding/binding.js";
import { unprovisioned, unprovisionedPhase } from "../binding/unprovisioned.js";
import { declarationSite } from "../declaration/callsite.js";
import { defer } from "../declaration/defer.js";
import { type Duration, encodeDuration, parseDurationMilliseconds } from "../delivery/duration.js";
import { encodeJsonSchema, validatePayload } from "../delivery/schema.js";
import {
  RealtimePublish,
  RealtimeSubscribe,
  ResourceType,
} from "../gen/proto/app/resources/v1/resources_pb.js";
import type { RealtimeProperties } from "../gen/proto/common/bindings/v1/bindings_pb.js";
import type { PatternParameters } from "../kv/pattern.js";
import { rpc } from "../runtime/rpc.js";
import type { TokenIssuer } from "./token.js";
import { resolveTransport } from "./transport.js";
import {
  type ChannelPattern,
  encodeWireChannel,
  isChannelNamespace,
  parseChannelPattern,
  type WireRefusal,
} from "./wire.js";

type MaybePromise<T> = T | Promise<T>;

/** One value for each `:parameter` of a pattern such as `"orders/:orderId"`. */
export type ChannelParams<TPattern extends string> = {
  [Name in PatternParameters<TPattern>]: string;
};

/** What a `subscribe` rule decides from: the caller's auth, the channel's params and the request. */
export interface SubscribeContext<TAuth, TParams> {
  /** What `authorize` returned for this request. */
  auth: TAuth;
  /** The params the caller asked for; on a `wildcard` pattern, trailing ones may be missing. */
  params: TParams;
  /** The request the realtime handler received. */
  request: Request;
}

/** What a `publish` rule decides from: a {@link SubscribeContext} and the event's body. */
export interface PublishContext<TAuth, TParams, TBody> extends SubscribeContext<TAuth, TParams> {
  /** The event the caller asked to publish, as its schema parsed it. */
  body: TBody;
}

/** The parts of a channel's declaration its rules are typed from. */
export interface ChannelShape {
  /** A Standard Schema every event on the channel is validated against. */
  schema: StandardSchemaV1;
  /** Lets a subscriber leave off trailing params, receiving every channel under the rest. */
  wildcard?: boolean;
  /**
   * `"public"`, which anyone may subscribe to, or a rule answering whether a caller may;
   * a rule needs `authorize` on the resource.
   */
  subscribe: unknown;
  /**
   * A rule answering whether a caller may publish from a browser; it needs `authorize` on
   * the resource. Without it, only the server publishes.
   */
  publish?: unknown;
}

type SubscribeParams<TPattern extends string, TChannel> = TChannel extends { wildcard: true }
  ? Partial<ChannelParams<TPattern>>
  : ChannelParams<TPattern>;

type BodyOf<TChannel> = TChannel extends { schema: infer TSchema extends StandardSchemaV1 }
  ? StandardSchemaV1.InferOutput<TSchema>
  : never;

type BodyInputOf<TChannel> = TChannel extends { schema: infer TSchema extends StandardSchemaV1 }
  ? StandardSchemaV1.InferInput<TSchema>
  : never;

/** The access rules of one channel pattern. */
export interface ChannelRules<TAuth, TPattern extends string, TChannel> {
  /** `"public"`, which anyone may subscribe to, or a rule answering whether this caller may. */
  subscribe:
    | "public"
    | ((
        context: SubscribeContext<TAuth, SubscribeParams<TPattern, TChannel>>,
      ) => MaybePromise<boolean>);
  /** A rule answering whether this caller may publish from a browser; without it, only the server publishes. */
  publish?: (
    context: PublishContext<TAuth, ChannelParams<TPattern>, BodyOf<TChannel>>,
  ) => MaybePromise<boolean>;
}

/** How a realtime resource is declared. */
export interface RealtimeOptions<TAuth, TChannels extends Record<string, ChannelShape>> {
  /**
   * Identifies the caller of the realtime handler from its request, with whatever auth the
   * app already uses, and answers `null` for nobody. Its result is `auth` in every rule;
   * its `id`, when a string or number, is the `sub` of every token minted for the caller.
   */
  authorize?: (request: Request) => MaybePromise<TAuth | null | undefined>;
  /** How long each minted token lives, from 10 to 300 seconds; without it, 60 seconds. */
  tokenTtl?: Duration;
  /**
   * The channel patterns, each at most 4 `/`-separated segments that are each a literal
   * or a `:param`, mapped to the event schema and access rules of every channel it names.
   */
  channels: {
    [TPattern in keyof TChannels]: {
      [Key in keyof TChannels[TPattern]]: TChannels[TPattern][Key];
    } & ChannelRules<TAuth, TPattern & string, TChannels[TPattern]>;
  };
}

/** The `{ params, body }` a publish on `TPattern` takes. */
export type PublishMessage<TPattern extends string, TChannel> = [
  PatternParameters<TPattern>,
] extends [never]
  ? { params?: Record<string, never>; body: BodyInputOf<TChannel> }
  : { params: ChannelParams<TPattern>; body: BodyInputOf<TChannel> };

/** A channel pattern as the resource declared it, its rules untyped. */
export interface DeclaredChannel {
  pattern: ChannelPattern;
  schema: StandardSchemaV1;
  wildcard: boolean;
  subscribe: "public" | ((context: SubscribeContext<unknown, unknown>) => MaybePromise<boolean>);
  publish?: (context: PublishContext<unknown, unknown, unknown>) => MaybePromise<boolean>;
}

/** What the realtime handler and publish read of a declared resource. */
export interface RealtimeRuntime extends TokenIssuer {
  channels: Map<string, DeclaredChannel>;
  authorize?: (request: Request) => MaybePromise<unknown>;
  properties(access: string): RealtimeProperties;
}

/**
 * Why `publish` refused an event: the pattern was never declared, the params are not all
 * strings, a param is missing, unknown, empty or over 30 bytes, the body fails the
 * pattern's schema, or the event is over 240 KiB.
 */
export type PublishRefusal =
  | "unknown-pattern"
  | "invalid-params"
  | "invalid-body"
  | "body-too-large"
  | WireRefusal;

/** Thrown by `publish` when the pattern, params or body cannot be published, naming why. */
export class RealtimePublishError extends Error {
  override name = "RealtimePublishError";
  /**
   * The reason: `unknown-pattern`, `invalid-params`, `missing-param`, `unknown-param`,
   * `empty-value`, `value-too-long`, `invalid-body` or `body-too-large`.
   */
  readonly code: PublishRefusal;

  constructor(code: PublishRefusal, message: string) {
    super(message);
    this.code = code;
  }
}

const runtimes = new WeakMap<object, RealtimeRuntime>();

/** Reads what the realtime handler serves of `rt`, throwing for one `realtime()` did not declare. */
export function readRuntime(rt: Realtime): RealtimeRuntime {
  const runtime = runtimes.get(rt);
  if (!runtime) throw new Error("the realtime handler takes a resource declared by realtime()");
  return runtime;
}

const minTtlSeconds = 10;
const maxTtlSeconds = 300;
const defaultTtl: Duration = "60s";

/** A declared realtime resource, and the handle its channels are published through. */
export class Realtime<
  TAuth = unknown,
  TChannels extends Record<string, ChannelShape> = Record<string, ChannelShape>,
> {
  /** The name this resource was declared under, which begins every channel. */
  readonly name: string;
  /** Type-only: the auth `authorize` answers and the declared channels, for a client's types. */
  declare readonly types?: { auth: TAuth; channels: TChannels };

  constructor(name: string, options: RealtimeOptions<TAuth, TChannels>) {
    this.name = name;
    const what = `realtime("${name}")`;
    if (!isChannelNamespace(name)) {
      throw new Error(
        `${what}: the name begins every channel, so it is letters, digits and -, at most 50 characters, starting and ending with a letter or digit`,
      );
    }
    const ttl = options.tokenTtl ?? defaultTtl;
    const ttlSeconds = parseDurationMilliseconds(ttl) / 1_000;
    if (!Number.isInteger(ttlSeconds) || ttlSeconds < minTtlSeconds || ttlSeconds > maxTtlSeconds) {
      throw new Error(
        `${what}: token ttl ${ttlSeconds}s is outside ${minTtlSeconds}s to ${maxTtlSeconds}s in whole seconds: a token lives long enough to open a socket and no longer`,
      );
    }
    const channels = new Map<string, DeclaredChannel>();
    for (const [written, declared] of Object.entries(
      options.channels as Record<string, ChannelShape>,
    )) {
      let pattern: ChannelPattern;
      try {
        pattern = parseChannelPattern(written);
      } catch (cause) {
        throw new Error(`${what}: ${(cause as Error).message}`);
      }
      if (!declared.schema?.["~standard"]) {
        throw new Error(
          `${what}: channel "${written}" takes a Standard Schema as its schema, and was given none`,
        );
      }
      if (!options.authorize && (declared.subscribe !== "public" || declared.publish)) {
        throw new Error(
          `${what}: channel "${written}" declares a rule, which decides from the caller's auth, and the resource declares no authorize to say who calls`,
        );
      }
      channels.set(written, {
        pattern,
        schema: declared.schema,
        wildcard: declared.wildcard ?? false,
        subscribe: declared.subscribe as DeclaredChannel["subscribe"],
        publish: declared.publish as DeclaredChannel["publish"],
      });
    }

    if (process.env.OCEL_PHASE === "discovery") {
      const source = declarationSite();
      defer(
        rpc.resource.declare({
          resource: { name, type: ResourceType.REALTIME },
          config: {
            case: "realtime",
            value: {
              tokenTtl: encodeDuration(ttl),
              channels: [...channels].map(([written, channel]) => ({
                pattern: written,
                wildcard: channel.wildcard,
                schema: encodeJsonSchema(channel.schema),
                subscribe:
                  channel.subscribe === "public"
                    ? RealtimeSubscribe.PUBLIC
                    : RealtimeSubscribe.RULE,
                publish: channel.publish ? RealtimePublish.RULE : RealtimePublish.SERVER,
                source,
              })),
            },
          },
          source,
        }),
      );
    }

    runtimes.set(this, {
      name,
      ttlSeconds,
      channels,
      authorize: options.authorize,
      properties: (access) => {
        if (unprovisionedPhase()) throw unprovisioned(what, access);
        return getConfig(name, "realtime");
      },
    });
  }

  /**
   * Publishes `body` on the channel `params` fill in `pattern`, to every subscriber, after
   * validating it against the pattern's schema. Every param is required. Throws a
   * {@link RealtimePublishError} for a pattern, params or body that cannot be published.
   */
  async publish<TPattern extends keyof TChannels & string>(
    pattern: TPattern,
    message: PublishMessage<TPattern, TChannels[TPattern]>,
  ): Promise<void> {
    const runtime = readRuntime(this);
    const transport = resolveTransport(runtime.properties("publish"), runtime);
    const prepared = await prepareEvent(runtime, pattern, message.params ?? {}, message.body);
    if ("refused" in prepared) {
      throw new RealtimePublishError(
        prepared.refused,
        `realtime("${this.name}") cannot publish on "${pattern}": ${prepared.reason}`,
      );
    }
    await transport.signPublish(prepared.channel)(prepared.envelope);
  }
}

/** An event ready for its transport, or why it cannot be published. */
export type PreparedEvent =
  | { channel: string; body: unknown; envelope: string }
  | { refused: PublishRefusal; reason: string };

const maxEventBytes = 240 * 1024;

/** Whether `params` is an object mapping each name to a string. */
export function isParams(params: unknown): params is Record<string, string> {
  return (
    params !== null &&
    typeof params === "object" &&
    !Array.isArray(params) &&
    Object.values(params).every((value) => typeof value === "string")
  );
}

/**
 * Validates `body` against `channel`'s schema and encodes the envelope it is published in
 * on `wire`, refusing a body the schema rejects or an event over 240 KiB.
 */
export async function encodeEvent(
  channel: DeclaredChannel,
  wire: string,
  body: unknown,
): Promise<PreparedEvent> {
  const validation = await validatePayload(channel.schema, body);
  if (!validation.ok)
    return { refused: "invalid-body", reason: `the body fails its schema: ${validation.message}` };
  const envelope = JSON.stringify({
    v: 1,
    id: randomBytes(16).toString("hex"),
    ch: wire,
    ts: Date.now(),
    kind: "live",
    data: validation.value,
  });
  if (new TextEncoder().encode(envelope).byteLength > maxEventBytes) {
    return { refused: "body-too-large", reason: `an event is at most ${maxEventBytes} bytes` };
  }
  return { channel: wire, body: validation.value, envelope };
}

/** Prepares the event the server publishes on `pattern`, or says why it cannot. */
export async function prepareEvent(
  runtime: RealtimeRuntime,
  pattern: string,
  params: unknown,
  body: unknown,
): Promise<PreparedEvent> {
  const channel = runtime.channels.get(pattern);
  if (!channel)
    return { refused: "unknown-pattern", reason: "no channel of that pattern is declared" };
  if (!isParams(params))
    return { refused: "invalid-params", reason: "params map each name to a string" };
  const wire = encodeWireChannel(runtime.name, channel.pattern, params, false);
  if ("refused" in wire)
    return { refused: wire.refused, reason: `the params are refused: ${wire.refused}` };
  return encodeEvent(channel, wire.channel, body);
}

/**
 * Declares a realtime resource named `name`: typed channels that browsers subscribe to
 * and the server publishes on, each pattern with the rules that decide who may.
 *
 * ```ts
 * export const rt = realtime("app", {
 *   authorize: async (req) => (await getSession(req)) ?? null,
 *   channels: {
 *     "orders/:orderId": {
 *       schema: OrderEvent,
 *       subscribe: async ({ auth, params }) => ownsOrder(auth, params.orderId),
 *     },
 *     status: { schema: Status, subscribe: "public" },
 *   },
 * });
 *
 * await rt.publish("orders/:orderId", { params: { orderId }, body: { status: "shipped" } });
 * ```
 *
 * Serve it to browsers with `createRealtimeHandler` from `ocel/realtime/next`,
 * `ocel/realtime/express` or `ocel/realtime/hono`.
 */
export function realtime<TAuth, TChannels extends Record<string, ChannelShape>>(
  name: string,
  options: RealtimeOptions<TAuth, TChannels>,
): Realtime<TAuth, TChannels> {
  return new Realtime(name, options);
}
