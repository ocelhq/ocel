import type { StandardSchemaV1 } from "@standard-schema/spec";
import { RealtimeError } from "./client-error.js";
import { loadTransport, type TransportConnection } from "./client-transport.js";
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
  ts: number;
}

/**
 * The client's connection: `connecting` until its socket first opens, `connected` while it
 * is open, `reconnecting` after it dropped, until every live subscription is authorized and
 * subscribed again.
 */
export type RealtimeClientState = "connecting" | "connected" | "reconnecting";

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

type Op =
  | { op: "subscribe"; subscription: Subscription }
  | {
      op: "publish";
      pattern: string;
      params: Record<string, string>;
      body: unknown;
      settle: (error?: RealtimeError) => void;
    };

interface HandlerAnswer {
  transport: string;
  url: string;
  host?: string;
  connect?: { token: string; expiresAt: number };
  grants: { i: number; wire: string; token?: string }[];
  denied: { i: number; code: string }[];
}

const maxOpsPerRequest = 50;
const initialBackoffMs = 500;
const maxBackoffMs = 30_000;
const retriableDenials = new Set(["rule-error", "publish-failed"]);

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
  let state: RealtimeClientState = "connecting";
  const stateListeners = new Set<(state: RealtimeClientState) => void>();
  const subscriptions = new Map<string, Subscription>();
  let queue: Op[] = [];
  let isPumping = false;
  let isClosed = false;
  let connection: TransportConnection | undefined;
  let failedAttempts = 0;
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  let nextId = 0;

  const setState = (next: RealtimeClientState) => {
    if (state === next) return;
    state = next;
    for (const listener of stateListeners) listener(next);
  };

  const failSubscription = (subscription: Subscription, error: RealtimeError) => {
    if (!error.retriable) subscriptions.delete(subscription.id);
    subscription.onError?.(error);
  };

  const scheduleRetry = () => {
    if (isClosed || retryTimer !== undefined) return;
    const ceiling = Math.min(maxBackoffMs, initialBackoffMs * 2 ** failedAttempts);
    failedAttempts += 1;
    retryTimer = setTimeout(() => {
      retryTimer = undefined;
      for (const subscription of subscriptions.values()) {
        const isQueued = queue.some(
          (op) => op.op === "subscribe" && op.subscription === subscription,
        );
        if (!subscription.isSubscribed && !isQueued) queue.push({ op: "subscribe", subscription });
      }
      void pump();
    }, Math.random() * ceiling);
  };

  const onDrop = (error: RealtimeError) => {
    connection = undefined;
    setState("reconnecting");
    for (const subscription of subscriptions.values()) {
      subscription.isSubscribed = false;
      subscription.onError?.(error);
    }
    scheduleRetry();
  };

  const readHeaders = async () =>
    typeof options.headers === "function" ? await options.headers() : (options.headers ?? {});

  const request = async (
    body: unknown,
  ): Promise<{ answer: HandlerAnswer } | { error: RealtimeError }> => {
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
    let response: Response;
    try {
      response = await fetch(options.url, {
        method: "POST",
        headers: { ...headers, "content-type": "application/json" },
        body: JSON.stringify(body),
      });
    } catch (cause) {
      return {
        error: new RealtimeError(
          "handler-unreachable",
          true,
          `the realtime handler at ${options.url} could not be reached: ${(cause as Error).message}`,
        ),
      };
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
      return { answer: (await response.json()) as HandlerAnswer };
    } catch (cause) {
      return {
        error: new RealtimeError(
          "handler-unreachable",
          true,
          `the realtime handler at ${options.url} sent no JSON answer: ${(cause as Error).message}`,
        ),
      };
    }
  };

  const subscribeGranted = async (subscription: Subscription, wire: string, token: string) => {
    if (!connection || subscriptions.get(subscription.id) !== subscription) return;
    try {
      await connection.subscribe(subscription.id, wire, token, (text) => {
        let envelope: { id: string; ch: string; ts: number; data: unknown };
        try {
          envelope = JSON.parse(text);
        } catch {
          return;
        }
        subscription.handler(envelope.data, {
          id: envelope.id,
          channel: envelope.ch,
          ts: envelope.ts,
        });
      });
      subscription.isSubscribed = subscriptions.get(subscription.id) === subscription;
    } catch (error) {
      failSubscription(subscription, error as RealtimeError);
    }
  };

  const send = async (batch: Op[]) => {
    const live = batch.filter(
      (op) => op.op === "publish" || subscriptions.get(op.subscription.id) === op.subscription,
    );
    if (live.length === 0) return;
    const needsConnection = !connection && live.some((op) => op.op === "subscribe");
    const result = await request({
      ...(needsConnection ? { connect: true } : {}),
      ops: live.map((op) =>
        op.op === "subscribe"
          ? { op: "subscribe", pattern: op.subscription.pattern, params: op.subscription.params }
          : { op: "publish", pattern: op.pattern, params: op.params, body: op.body },
      ),
    });
    if ("error" in result) {
      for (const op of live) {
        if (op.op === "publish") op.settle(result.error);
        else failSubscription(op.subscription, result.error);
      }
      if (result.error.retriable && live.some((op) => op.op === "subscribe")) scheduleRetry();
      return;
    }
    const { answer } = result;
    for (const { i, code } of answer.denied) {
      const op = live[i];
      if (!op) continue;
      const error = new RealtimeError(
        code,
        retriableDenials.has(code),
        op.op === "subscribe"
          ? `the realtime handler denied the subscribe on "${op.subscription.pattern}": ${code}`
          : `the realtime handler denied the publish on "${op.pattern}": ${code}`,
      );
      if (op.op === "publish") op.settle(error);
      else failSubscription(op.subscription, error);
    }
    const subscribes: { subscription: Subscription; wire: string; token: string }[] = [];
    for (const { i, wire, token } of answer.grants) {
      const op = live[i];
      if (op?.op === "publish") op.settle();
      else if (op?.op === "subscribe" && token)
        subscribes.push({ subscription: op.subscription, wire, token });
    }
    if (subscribes.length === 0) return;
    if (!connection) {
      if (!answer.connect) {
        scheduleRetry();
        return;
      }
      try {
        const transport = await loadTransport(answer.transport);
        connection = await transport.connect(
          { url: answer.url, host: answer.host, token: answer.connect.token },
          onDrop,
        );
      } catch (error) {
        for (const { subscription } of subscribes)
          failSubscription(subscription, error as RealtimeError);
        if ((error as RealtimeError).retriable) scheduleRetry();
        return;
      }
      if (isClosed) {
        connection.close();
        connection = undefined;
        return;
      }
      failedAttempts = 0;
      setState("connected");
    }
    await Promise.all(
      subscribes.map(({ subscription, wire, token }) =>
        subscribeGranted(subscription, wire, token),
      ),
    );
  };

  const pump = async () => {
    if (isPumping) return;
    isPumping = true;
    await Promise.resolve();
    try {
      while (queue.length > 0 && !isClosed) {
        await send(queue.splice(0, maxOpsPerRequest));
      }
    } finally {
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
      queue.push({ op: "subscribe", subscription });
      void pump();
      return () => {
        if (subscriptions.get(subscription.id) !== subscription) return;
        subscriptions.delete(subscription.id);
        queue = queue.filter((op) => op.op !== "subscribe" || op.subscription !== subscription);
        connection?.unsubscribe(subscription.id);
        subscription.isSubscribed = false;
      };
    },
    publish(pattern, message) {
      if (isClosed) return Promise.reject(refuseClosed());
      return new Promise<void>((resolve, reject) => {
        queue.push({
          op: "publish",
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
      for (const op of queue) if (op.op === "publish") op.settle(refuseClosed());
      queue = [];
      connection?.close();
      connection = undefined;
    },
  };
}
