import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { type WebSocket, WebSocketServer } from "ws";
import { findRefusal } from "./realtime-gateway.js";
import { readRealtimeBindingFixture } from "./realtime-vectors.js";

/** How the fake carries an event in a `data` frame: AppSync's docs show more than one form. */
export type FakeAppSyncEventForm = "string" | "array" | "double-encoded";

/** What a test can force on the fake. */
export interface FakeAppSyncOptions {
  eventForm?: FakeAppSyncEventForm;
  keepAliveMilliseconds?: number;
  connectionTimeoutMilliseconds?: number;
  connectionLifetimeMilliseconds?: number;
  clockOffsetSeconds?: number;
}

/** One connect or subscribe the fake received, with the authorization it carried. */
export interface FakeAppSyncAuthorization {
  host?: string;
  Authorization?: string;
}

/** An in-repo stand-in for an AppSync Event API's realtime socket. */
export interface FakeAppSync {
  binding: string;
  host: string;
  url: string;
  connects: FakeAppSyncAuthorization[];
  subscribes: { id: string; channel: string; authorization: FakeAppSyncAuthorization }[];
  options: FakeAppSyncOptions;
  broadcast(channel: string, envelope: string): void;
  breakBroadcast(channel: string): void;
  silence(): void;
  close(): Promise<void>;
}

const socketPath = "/event/realtime";
const subprotocol = "aws-appsync-event-ws";
const fixture = JSON.parse(readRealtimeBindingFixture());

function readHeader(protocols: string[]): FakeAppSyncAuthorization {
  const offered = protocols.find((protocol) => protocol.startsWith("header-"));
  if (!offered) return {};
  return JSON.parse(Buffer.from(offered.slice(7), "base64url").toString("utf8"));
}

function isSubscribedTo(subscribed: string, channel: string): boolean {
  if (subscribed === channel) return true;
  return subscribed.endsWith("/*") && channel.startsWith(subscribed.slice(0, -1));
}

function encodeEvent(envelope: string, form: FakeAppSyncEventForm): unknown {
  switch (form) {
    case "array":
      return [envelope];
    case "double-encoded":
      return JSON.stringify(envelope);
    default:
      return envelope;
  }
}

/**
 * Serves a fake AppSync Event API: `header-` subprotocol auth carrying the API's host,
 * per-subscribe authorization checked as the Ocel authorizer checks it, `ka` keep-alives,
 * and the forced failures AppSync can answer with.
 */
export async function serveFakeAppSync(options: FakeAppSyncOptions = {}): Promise<FakeAppSync> {
  const connects: FakeAppSync["connects"] = [];
  const subscribes: FakeAppSync["subscribes"] = [];
  const live = new Map<WebSocket, Map<string, string>>();
  const timers = new Set<ReturnType<typeof setTimeout>>();
  let isSilent = false;
  const nowSeconds = () => Date.now() / 1_000 + (options.clockOffsetSeconds ?? 0);
  const server = createServer((_req, res) => {
    res.statusCode = 404;
    res.end();
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address() as AddressInfo;
  const host = `127.0.0.1:${port}`;
  const url = `ws://${host}${socketPath}`;
  const socketServer = new WebSocketServer({
    server,
    path: socketPath,
    handleProtocols: (protocols) => (protocols.has(subprotocol) ? subprotocol : false),
  });
  const send = (socket: WebSocket, frame: unknown) => socket.send(JSON.stringify(frame));
  socketServer.on("connection", (socket, req) => {
    const header = readHeader(
      (req.headers["sec-websocket-protocol"] ?? "").split(",").map((protocol) => protocol.trim()),
    );
    connects.push(header);
    const refusal =
      header.host !== host
        ? "the connect names another host"
        : findRefusal(header.Authorization, host, "connect", "/app", nowSeconds());
    if (refusal) {
      send(socket, {
        type: "connection_error",
        errors: [{ errorType: "UnauthorizedException", message: refusal }],
      });
      socket.close(1008, "unauthorized");
      return;
    }
    const subscriptions = new Map<string, string>();
    const keepAlive = setInterval(() => {
      if (!isSilent) send(socket, { type: "ka" });
    }, options.keepAliveMilliseconds ?? 60_000);
    timers.add(keepAlive);
    if (options.connectionLifetimeMilliseconds !== undefined) {
      const lifetime = setTimeout(() => socket.close(1000), options.connectionLifetimeMilliseconds);
      timers.add(lifetime);
    }
    socket.on("close", () => {
      clearInterval(keepAlive);
      live.delete(socket);
    });
    socket.on("message", (raw) => {
      const frame = JSON.parse(raw.toString());
      if (frame.type === "connection_init") {
        live.set(socket, subscriptions);
        send(socket, {
          type: "connection_ack",
          connectionTimeoutMs: options.connectionTimeoutMilliseconds ?? 300_000,
        });
      } else if (frame.type === "subscribe") {
        const authorization: FakeAppSyncAuthorization = frame.authorization ?? {};
        subscribes.push({ id: frame.id, channel: frame.channel, authorization });
        const denied = findRefusal(
          authorization.Authorization,
          host,
          "subscribe",
          frame.channel,
          nowSeconds(),
        );
        if (denied) {
          send(socket, {
            type: "subscribe_error",
            id: frame.id,
            errors: [{ errorType: "UnauthorizedException", message: denied }],
          });
          return;
        }
        subscriptions.set(frame.id, frame.channel);
        send(socket, { type: "subscribe_success", id: frame.id });
      } else if (frame.type === "unsubscribe") {
        subscriptions.delete(frame.id);
        send(socket, { type: "unsubscribe_success", id: frame.id });
      }
    });
  });
  const eachSubscriber = (channel: string, reach: (socket: WebSocket, id: string) => void) => {
    for (const [socket, subscriptions] of live) {
      for (const [id, subscribed] of subscriptions) {
        if (isSubscribedTo(subscribed, channel)) reach(socket, id);
      }
    }
  };
  return {
    binding: JSON.stringify({
      name: "realtime--app",
      realtime: { ...fixture.realtime, transport: "REALTIME_TRANSPORT_APPSYNC_EVENTS", url, host },
    }),
    host,
    url,
    connects,
    subscribes,
    options,
    broadcast(channel, envelope) {
      eachSubscriber(channel, (socket, id) =>
        send(socket, {
          type: "data",
          id,
          event: encodeEvent(envelope, options.eventForm ?? "string"),
        }),
      );
    },
    breakBroadcast(channel) {
      eachSubscriber(channel, (socket, id) =>
        send(socket, {
          type: "broadcast_error",
          id,
          errors: [
            {
              errorType: "MessageProcessingError",
              message: "There was an error processing the message",
            },
          ],
        }),
      );
    },
    silence() {
      isSilent = true;
    },
    close: () =>
      new Promise<void>((resolve, reject) => {
        if (!server.listening) {
          resolve();
          return;
        }
        for (const timer of timers) clearTimeout(timer);
        for (const socket of live.keys()) socket.terminate();
        for (const client of socketServer.clients) client.terminate();
        socketServer.close();
        server.close((err) => (err ? reject(err) : resolve()));
        server.closeAllConnections();
      }),
  };
}
