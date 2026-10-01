import type { StandardSchemaV1 } from "@standard-schema/spec";
import { RealtimeError, type RealtimeErrorCode } from "./client-error.js";
import { loadTransport, type TransportConnection } from "./client-transport.js";
import type { RealtimeBatchAnswer } from "./handler.js";
import type { ChannelShape } from "./realtime.js";

export { RealtimeError, type RealtimeErrorCode } from "./client-error.js";

/**
 * The client-side types of one channel pattern: the event its subscribers receive, the body
 * a browser publishes when a `publish` rule exists, and whether subscribers may leave off
 * trailing params. `ocel generate` writes these for a realtime resource declared in Go,
 * Python or Rust.
 */
export interface ChannelTypes {
  /** The event every subscriber receives. */
  event: unknown;
  /** The body a browser publishes; absent where only the server publishes. */
  publish?: unknown;
  /** Present when subscribers may leave off trailing params. */
  wildcard?: true;
}

/** The client-side types of a realtime resource's channels, keyed by pattern. */
export type RealtimeTypes = Record<string, ChannelTypes>;

type ChannelTypesOf<TChannel> = TChannel extends { schema: infer TSchema extends StandardSchemaV1 }
  ? { event: StandardSchemaV1.InferOutput<TSchema> } & (TChannel extends { wildcard: true }
      ? { wildcard: true }
      : unknown) &
      (TChannel extends { publish: unknown }
        ? { publish: StandardSchemaV1.InferInput<TSchema> }
        : unknown)
  : never;

/**
 * The channel types a client is typed from: those of a resource `realtime()` declared, when
 * given its type, or the {@link RealtimeTypes} `ocel generate` wrote.
 */
export type ClientChannels<T> = T extends {
  readonly name: string;
  readonly types?: { channels: infer TChannels extends Record<string, ChannelShape> };
}
  ? { [TPattern in keyof TChannels & string]: ChannelTypesOf<TChannels[TPattern]> }
  : T extends RealtimeTypes
    ? T
    : never;

type ParamList<TPattern extends string> = TPattern extends `${infer Head}/${infer Rest}`
  ? [...ParamList<Head>, ...ParamList<Rest>]
  : TPattern extends `:${infer Name}`
    ? [Name]
    : [];

type Filled<TNames extends string> = { [Name in TNames]: string };
type Left<TNames extends string> = { [Name in TNames]?: undefined };

type TrailingParams<TNames extends string[], TLeft extends string = never> = TNames extends [
  ...infer Init extends string[],
  infer Last extends string,
]
  ? (Filled<TNames[number]> & Left<TLeft>) | TrailingParams<Init, TLeft | Last>
  : Left<TLeft>;

type ParamsOption<TParams> = [keyof TParams] extends [never]
  ? { params?: Record<string, never> }
  : Record<never, never> extends TParams
    ? { params?: TParams }
    : { params: TParams };

/** The params a subscribe on `TPattern` takes: every one, or on a `wildcard` pattern a leading run. */
export type SubscribeParams<TPattern extends string, TChannel> = TChannel extends { wildcard: true }
  ? TrailingParams<ParamList<TPattern>>
  : Filled<ParamList<TPattern>[number]>;

/** How one subscription is made. */
export type SubscribeOptions<TPattern extends string, TChannel> = ParamsOption<
  SubscribeParams<TPattern, TChannel>
> & {
  /**
   * Hears why the subscription failed. When `error.retriable` is false the subscription has
   * ended; when true the client keeps trying on its own.
   */
  onError?: (error: RealtimeError) => void;
};

/** The `{ params, body }` a browser publish on `TPattern` takes; every param is required. */
export type ClientPublishMessage<TPattern extends string, TChannel> = ParamsOption<
  Filled<ParamList<TPattern>[number]>
> & { body: TChannel extends { publish: infer TBody } ? TBody : never };

/** What each event carries besides its body, from its envelope. */
export interface EventMeta {
  /** The event's id, 32 lowercase hex characters. */
  id: string;
  /** The wire channel it was published on, such as `/app/orders/o-1`. */
  channel: string;
  /** When the server published it, in epoch milliseconds. */
  publishedAt: number;
}

/**
 * The client's socket: `idle` while no subscription needs one, `connecting` until it opens,
 * `connected` while it is open, `reconnecting` after it dropped with subscriptions left to
 * restore, until it opens again, and `closed` once `close` is called. Whether each
 * subscription is restored is reported through its own `onError`, not here.
 */
export type RealtimeClientState = "idle" | "connecting" | "connected" | "reconnecting" | "closed";

/** Where a realtime client finds the app's realtime handler. */
export interface RealtimeClientOptions {
  /** The URL the app mounted its realtime handler at, such as `"/api/realtime"`. */
  url: string;
  /**
   * Headers sent with every request to the handler, such as a bearer token for a handler on
   * another origin; a function is called before each request, so a token can be refreshed.
   */
  headers?:
    | Record<string, string>
    | (() => Record<string, string> | Promise<Record<string, string>>);
}

type Publishable<TChannels> = {
  [TPattern in keyof TChannels]: TChannels[TPattern] extends { publish: unknown }
    ? TPattern
    : never;
}[keyof TChannels] &
  string;

/** A browser's connection to one realtime resource. */
export interface RealtimeClient<TChannels> {
  /** The connection's current state. */
  readonly state: RealtimeClientState;
  /** Calls `listener` with each new state; returns the function that stops it. */
  onStateChange(listener: (state: RealtimeClientState) => void): () => void;
  /**
   * Subscribes to the channel `params` fill in `pattern`, calling `handler` with each event.
   * Subscriptions made together are authorized in one request, and every live one is
   * authorized again after a reconnect. Returns the function that unsubscribes.
   */
  subscribe<TPattern extends keyof TChannels & string>(
    pattern: TPattern,
    options: SubscribeOptions<TPattern, TChannels[TPattern]>,
    handler: (event: EventOf<TChannels[TPattern]>, meta: EventMeta) => void,
  ): () => void;
  /**
   * Publishes `body` on the channel `params` fill in `pattern`, through the app's realtime
   * handler, which runs the pattern's `publish` rule. Rejects with a {@link RealtimeError}.
   */
  publish<TPattern extends Publishable<TChannels>>(
    pattern: TPattern,
    message: ClientPublishMessage<TPattern, TChannels[TPattern]>,
  ): Promise<void>;
  /** Ends every subscription and closes the socket. */
  close(): void;
}

type EventOf<TChannel> = TChannel extends { event: infer TEvent } ? TEvent : never;

interface Subscription {
  id: string;
  pattern: string;
  params: Record<string, string>;
  handler: (event: unknown, meta: EventMeta) => void;
  onError?: (error: RealtimeError) => void;
  isSubscribed: boolean;
}

type Operation =
  | { kind: "subscribe"; subscription: Subscription }
  | {
      kind: "publish";
      pattern: string;
      params: Record<string, string>;
      body: unknown;
      settle: (error?: RealtimeError) => void;
    };

interface GrantedSubscribe {
  subscription: Subscription;
  wire: string;
  token: string;
}

const maxOperationsPerRequest = 50;
const initialBackoffMilliseconds = 500;
const maxBackoffMilliseconds = 30_000;
const requestDeadlineMilliseconds = 10_000;
const retriableDenials = new Set<RealtimeErrorCode>(["rule-error", "publish-failed"]);

function dropUndefined(params: Record<string, string | undefined> | undefined) {
  const out: Record<string, string> = {};
  for (const [name, value] of Object.entries(params ?? {})) {
    if (value !== undefined) out[name] = value;
  }
  return out;
}

function isRetriableStatus(status: number): boolean {
  return status >= 500 || status === 408 || status === 429;
}

function readPattern(operation: Operation): string {
  return operation.kind === "subscribe" ? operation.subscription.pattern : operation.pattern;
}

function describeOperation(operation: Operation) {
  return operation.kind === "subscribe"
    ? {
        op: "subscribe",
        pattern: operation.subscription.pattern,
        params: operation.subscription.params,
      }
    : { op: "publish", pattern: operation.pattern, params: operation.params, body: operation.body };
}

function deliver(subscription: Subscription, text: string) {
  let envelope: { id: string; ch: string; ts: number; data: unknown };
  try {
    envelope = JSON.parse(text);
  } catch {
    return;
  }
  subscription.handler(envelope.data, {
    id: envelope.id,
    channel: envelope.ch,
    publishedAt: envelope.ts,
  });
}

/**
 * Connects a browser to the realtime resource whose handler the app mounted at `url`.
 * `T` is the resource's type, `typeof rt` for one declared with `realtime()`, which the
 * client imports as a type alone so no server code reaches the bundle, or the
 * {@link RealtimeTypes} `ocel generate` wrote for a resource declared in Go, Python or Rust.
 *
 * ```ts
 * import type { rt } from "../server/realtime";
 *
 * const live = createRealtimeClient<typeof rt>({ url: "/api/realtime" });
 * const stop = live.subscribe("orders/:orderId", { params: { orderId } }, (event) => …);
 * await live.publish("rooms/:roomId", { params: { roomId }, body: { text: "hi" } });
 * ```
 */
export function createRealtimeClient<T>(
  options: RealtimeClientOptions,
): RealtimeClient<ClientChannels<T>> {
  let state: RealtimeClientState = "idle";
  const stateListeners = new Set<(state: RealtimeClientState) => void>();
  const subscriptions = new Map<string, Subscription>();
  let queue: Operation[] = [];
  let sending: Operation[] = [];
  let isPumping = false;
  let isClosed = false;
  let hasDropped = false;
  let connection: TransportConnection | undefined;
  let failedAttempts = 0;
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  let nextId = 0;

  const settleState = () => {
    let next: RealtimeClientState;
    if (isClosed) next = "closed";
    else if (connection) next = "connected";
    else if (subscriptions.size === 0) {
      hasDropped = false;
      next = "idle";
    } else next = hasDropped ? "reconnecting" : "connecting";
    if (state === next) return;
    state = next;
    for (const listener of stateListeners) listener(next);
  };

  const isLive = (subscription: Subscription) =>
    subscriptions.get(subscription.id) === subscription;

  const isAwaiting = (subscription: Subscription) =>
    [...queue, ...sending].some(
      (operation) => operation.kind === "subscribe" && operation.subscription === subscription,
    );

  const scheduleRetry = () => {
    if (isClosed || retryTimer !== undefined) return;
    const ceiling = Math.min(
      maxBackoffMilliseconds,
      initialBackoffMilliseconds * 2 ** failedAttempts,
    );
    failedAttempts += 1;
    retryTimer = setTimeout(() => {
      retryTimer = undefined;
      for (const subscription of subscriptions.values()) {
        if (!subscription.isSubscribed && !isAwaiting(subscription)) {
          queue.push({ kind: "subscribe", subscription });
        }
      }
      void pump();
    }, Math.random() * ceiling);
  };

  const failSubscription = (subscription: Subscription, error: RealtimeError) => {
    if (!isLive(subscription)) return;
    if (error.retriable) {
      scheduleRetry();
    } else {
      subscriptions.delete(subscription.id);
      settleState();
    }
    subscription.onError?.(error);
  };

  const failOperation = (operation: Operation, error: RealtimeError) => {
    if (operation.kind === "publish") operation.settle(error);
    else failSubscription(operation.subscription, error);
  };

  const onDrop = (error: RealtimeError) => {
    connection = undefined;
    hasDropped = true;
    for (const subscription of subscriptions.values()) subscription.isSubscribed = false;
    settleState();
    for (const subscription of [...subscriptions.values()]) subscription.onError?.(error);
    if (subscriptions.size > 0) scheduleRetry();
  };

  const readHeaders = async () =>
    typeof options.headers === "function" ? await options.headers() : (options.headers ?? {});

  const request = async (
    body: unknown,
  ): Promise<{ answer: RealtimeBatchAnswer } | { error: RealtimeError }> => {
    let headers: Record<string, string>;
    try {
      headers = await readHeaders();
    } catch (cause) {
      return {
        error: new RealtimeError(
          "headers-failed",
          true,
          `the headers for the realtime handler could not be read: ${(cause as Error).message}`,
        ),
      };
    }
    const controller = new AbortController();
    const deadline = setTimeout(() => controller.abort(), requestDeadlineMilliseconds);
    const unreachable = (cause: unknown, failure: string) =>
      new RealtimeError(
        "handler-unreachable",
        true,
        controller.signal.aborted
          ? `the realtime handler at ${options.url} did not answer within ${requestDeadlineMilliseconds}ms`
          : `the realtime handler at ${options.url} ${failure}: ${(cause as Error).message}`,
      );
    try {
      let response: Response;
      try {
        response = await fetch(options.url, {
          method: "POST",
          headers: { ...headers, "content-type": "application/json" },
          body: JSON.stringify(body),
          signal: controller.signal,
        });
      } catch (cause) {
        return { error: unreachable(cause, "could not be reached") };
      }
      if (!response.ok) {
        return {
          error: new RealtimeError(
            "handler-refused",
            isRetriableStatus(response.status),
            `the realtime handler at ${options.url} answered ${response.status}`,
          ),
        };
      }
      try {
        return { answer: (await response.json()) as RealtimeBatchAnswer };
      } catch (cause) {
        return { error: unreachable(cause, "sent no JSON answer") };
      }
    } finally {
      clearTimeout(deadline);
    }
  };

  const settleDenials = (live: Operation[], denied: RealtimeBatchAnswer["denied"]) => {
    for (const { i, code } of denied) {
      const operation = live[i];
      if (!operation) continue;
      failOperation(
        operation,
        new RealtimeError(
          code,
          retriableDenials.has(code),
          `the realtime handler denied the ${operation.kind} on "${readPattern(operation)}": ${code}`,
        ),
      );
    }
  };

  const settleGrants = (
    live: Operation[],
    grants: RealtimeBatchAnswer["grants"],
  ): GrantedSubscribe[] => {
    const granted: GrantedSubscribe[] = [];
    for (const { i, wire, token } of grants) {
      const operation = live[i];
      if (operation?.kind === "publish") {
        operation.settle();
      } else if (operation?.kind === "subscribe" && token) {
        granted.push({ subscription: operation.subscription, wire, token });
      } else if (operation?.kind === "subscribe") {
        failSubscription(
          operation.subscription,
          new RealtimeError(
            "invalid-grant",
            false,
            `the realtime handler granted the subscribe on "${operation.subscription.pattern}" without a token`,
          ),
        );
      }
    }
    return granted;
  };

  const connect = async (
    answer: RealtimeBatchAnswer,
    granted: GrantedSubscribe[],
  ): Promise<TransportConnection | undefined> => {
    if (!answer.connect) {
      scheduleRetry();
      return undefined;
    }
    let opened: TransportConnection;
    try {
      const transport = await loadTransport(answer.transport);
      opened = await transport.connect(
        { url: answer.url, host: answer.host, token: answer.connect.token },
        onDrop,
      );
    } catch (error) {
      for (const { subscription } of granted) {
        failSubscription(subscription, error as RealtimeError);
      }
      return undefined;
    }
    if (isClosed) {
      opened.close();
      return undefined;
    }
    connection = opened;
    failedAttempts = 0;
    hasDropped = false;
    settleState();
    return opened;
  };

  const subscribeGranted = async (
    opened: TransportConnection,
    { subscription, wire, token }: GrantedSubscribe,
  ) => {
    if (!isLive(subscription)) return;
    try {
      await opened.subscribe(subscription.id, wire, token, (text) => deliver(subscription, text));
      subscription.isSubscribed = isLive(subscription);
    } catch (error) {
      if (connection === opened) failSubscription(subscription, error as RealtimeError);
    }
  };

  const send = async (batch: Operation[]) => {
    const live = batch.filter(
      (operation) => operation.kind === "publish" || isLive(operation.subscription),
    );
    if (live.length === 0) return;
    const needsConnection = !connection && live.some((operation) => operation.kind === "subscribe");
    const result = await request({
      ...(needsConnection ? { connect: true } : {}),
      ops: live.map(describeOperation),
    });
    if ("error" in result) {
      for (const operation of live) failOperation(operation, result.error);
      return;
    }
    settleDenials(live, result.answer.denied);
    const granted = settleGrants(live, result.answer.grants);
    if (granted.length === 0) return;
    const opened = connection ?? (await connect(result.answer, granted));
    if (!opened) return;
    await Promise.all(granted.map((grant) => subscribeGranted(opened, grant)));
  };

  const pump = async () => {
    if (isPumping) return;
    isPumping = true;
    await Promise.resolve();
    try {
      while (queue.length > 0 && !isClosed) {
        sending = queue.splice(0, maxOperationsPerRequest);
        await send(sending);
      }
    } finally {
      sending = [];
      isPumping = false;
    }
  };

  const refuseClosed = () => new RealtimeError("closed", false, "the realtime client was closed");

  return {
    get state() {
      return state;
    },
    onStateChange(listener) {
      stateListeners.add(listener);
      return () => stateListeners.delete(listener);
    },
    subscribe(pattern, subscribeOptions, handler) {
      if (isClosed) throw refuseClosed();
      const subscription: Subscription = {
        id: `s${nextId++}`,
        pattern,
        params: dropUndefined(subscribeOptions.params as Record<string, string | undefined>),
        handler: handler as Subscription["handler"],
        onError: subscribeOptions.onError,
        isSubscribed: false,
      };
      subscriptions.set(subscription.id, subscription);
      queue.push({ kind: "subscribe", subscription });
      settleState();
      void pump();
      return () => {
        if (!isLive(subscription)) return;
        subscriptions.delete(subscription.id);
        queue = queue.filter(
          (operation) => operation.kind !== "subscribe" || operation.subscription !== subscription,
        );
        connection?.unsubscribe(subscription.id);
        subscription.isSubscribed = false;
        settleState();
      };
    },
    publish(pattern, message) {
      if (isClosed) return Promise.reject(refuseClosed());
      return new Promise<void>((resolve, reject) => {
        queue.push({
          kind: "publish",
          pattern,
          params: dropUndefined(message.params as Record<string, string> | undefined),
          body: message.body,
          settle: (error) => (error ? reject(error) : resolve()),
        });
        void pump();
      });
    },
    close() {
      isClosed = true;
      clearTimeout(retryTimer);
      subscriptions.clear();
      for (const operation of queue) {
        if (operation.kind === "publish") operation.settle(refuseClosed());
      }
      queue = [];
      connection?.close();
      connection = undefined;
      settleState();
    },
  };
}
