import { createPrivateKey, createPublicKey, randomBytes, sign, verify } from "node:crypto";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { Code, ConnectError } from "@connectrpc/connect";
import { connectNodeAdapter } from "@connectrpc/connect-node";
import { type WebSocket, WebSocketServer } from "ws";
import { bindingKey } from "../binding/binding.js";
import {
  type MintRequest,
  Operation,
  RealtimeService,
} from "../gen/proto/app/realtime/v1/realtime_pb.js";
import { readRealtimeBindingFixture } from "./realtime-vectors.js";

export interface PublishedEvent {
  realtime: string;
  channel: string;
  envelope: { v: number; id: string; ch: string; ts: number; kind: string; data: unknown };
}

interface GatewayPublishes {
  published: PublishedEvent[];
  status: number;
  relay(channel: string, envelope: string): void;
}

/** What the fake ocel runtime was asked to mint, and how it answers the next Mint. */
export interface FakeRuntime {
  mints: MintRequest[];
  mapChannel?: (channel: string) => string;
  mintRefusal?: ConnectError;
}

let publishingGateway: GatewayPublishes | undefined;
let mintingRuntime: FakeRuntime = { mints: [] };
let runtimeProxyAddress: Promise<string> | undefined;

const fixture = JSON.parse(readRealtimeBindingFixture());

function signFixtureToken(claims: Record<string, unknown>): string {
  const key = createPrivateKey({
    key: Buffer.concat([
      Buffer.from("302e020100300506032b657004220420", "hex"),
      Buffer.from(fixture.realtime.signingKey, "base64"),
    ]),
    format: "der",
    type: "pkcs8",
  });
  const encode = (value: unknown) => Buffer.from(JSON.stringify(value)).toString("base64url");
  const input = `${encode({ alg: "EdDSA", typ: "JWT" })}.${encode(claims)}`;
  return `${input}.${sign(null, Buffer.from(input), key).toString("base64url")}`;
}

function mint(req: MintRequest) {
  const runtime = mintingRuntime;
  runtime.mints.push(req);
  if (runtime.mintRefusal) throw runtime.mintRefusal;
  const bound = process.env[bindingKey(req.realtime, "realtime")];
  if (!bound) throw new ConnectError("no realtime binding was delivered", Code.FailedPrecondition);
  const host: string = JSON.parse(bound).realtime.host;
  const issuedAt = Math.floor(Date.now() / 1_000);
  const expiresAt = issuedAt + Number(req.ttl?.seconds ?? 0n);
  return {
    tokens: req.grants.map((grant) => ({
      token: signFixtureToken({
        iss: `ocel:rt:${req.realtime}`,
        aud: host,
        sub: req.subject,
        iat: issuedAt,
        exp: expiresAt,
        jti: randomBytes(16).toString("base64url"),
        ocel: {
          op: grant.operation === Operation.CONNECT ? "connect" : "subscribe",
          ch: grant.channel,
          ns: req.realtime,
        },
      }),
      channel: runtime.mapChannel?.(grant.channel) ?? grant.channel,
    })),
    expiresAt: { seconds: BigInt(expiresAt), nanos: 0 },
  };
}

function serveRuntimeProxy(): Promise<string> {
  const adapter = connectNodeAdapter({
    routes: (router) =>
      router.service(RealtimeService, {
        mint,
        publish(req) {
          const gateway = publishingGateway;
          if (!gateway)
            throw new ConnectError("no fake gateway is serving", Code.FailedPrecondition);
          gateway.published.push({
            realtime: req.realtime,
            channel: req.channel,
            envelope: JSON.parse(req.event),
          });
          if (gateway.status >= 300) {
            throw new ConnectError(
              `the gateway refused it with status ${gateway.status}`,
              Code.Unavailable,
            );
          }
          gateway.relay(req.channel, req.event);
          return {};
        },
      }),
  });
  const server = createServer(adapter);
  server.unref();
  return new Promise((resolve) =>
    server.listen(0, "127.0.0.1", () =>
      resolve(`http://127.0.0.1:${(server.address() as AddressInfo).port}`),
    ),
  );
}

/**
 * Points the SDK at a fake ocel runtime that mints tokens with the binding fixture's key, as
 * ocel's own runtime does, recording each Mint it is asked for.
 */
export async function serveFakeRuntime(): Promise<FakeRuntime> {
  const runtime: FakeRuntime = { mints: [] };
  mintingRuntime = runtime;
  process.env.OCEL_RUNTIME_ADDRESS = await (runtimeProxyAddress ??= serveRuntimeProxy());
  process.env.OCEL_SESSION_TOKEN = "session-token";
  return runtime;
}

export interface FakeGateway {
  runtime: FakeRuntime;
  binding: string;
  host: string;
  url: string;
  published: PublishedEvent[];
  sockets: WebSocket[];
  connectTokens: string[];
  closedForPolicy: string[];
  subscribes: { id: string; channel: string }[];
  dropSockets(): void;
  close(): Promise<void>;
}

const socketPath = "/event/realtime";
const subprotocol = "aws-appsync-event-ws";

export const appsyncBinding = JSON.stringify({
  name: fixture.name,
  realtime: { url: fixture.realtime.url, host: fixture.realtime.host },
});
export const appsyncHost: string = fixture.realtime.host;
export const appsyncURL: string = fixture.realtime.url;

export function readClaims(token: string): Record<string, unknown> & {
  ocel: { op: string; ch: string; ns: string };
} {
  const [header, payload, signature] = token.split(".");
  const key = createPublicKey({
    key: Buffer.concat([
      Buffer.from("302a300506032b6570032100", "hex"),
      Buffer.from(fixture.realtime.verifyKey, "base64"),
    ]),
    format: "der",
    type: "spki",
  });
  if (
    !verify(
      null,
      Buffer.from(`${header}.${payload}`),
      key,
      Buffer.from(signature ?? "", "base64url"),
    )
  ) {
    throw new Error(`token ${token} does not verify against the fixture's verify key`);
  }
  return JSON.parse(Buffer.from(payload ?? "", "base64url").toString("utf8"));
}

export interface FakeGatewayOptions {
  status?: number;
  acknowledgingConnections?: boolean;
  answeringSubscribes?: boolean;
  /**
   * Closes each socket for policy this long after it opened, as the gateway does when the
   * connect token expires, without waiting out a real token lifetime.
   */
  connectTokenLifetimeMs?: number;
  /** Pings each socket this often, and drops one whose pong is late by `pongTimeoutMs`. */
  pingIntervalMs?: number;
  pongTimeoutMs?: number;
}

/**
 * Answers why `token` does not admit `op` on `channel` at `host` as of `nowSeconds`, or
 * undefined when it does.
 */
export function findRefusal(
  token: string | undefined,
  host: string,
  op: string,
  channel: string,
  nowSeconds = Date.now() / 1_000,
): string | undefined {
  try {
    const claims = readClaims(token ?? "");
    if (claims.aud !== host) return "the token is for another host";
    if (Number(claims.exp) <= nowSeconds) return "the token has expired";
    if (claims.ocel.op !== op || claims.ocel.ch !== channel) {
      return `the token admits ${claims.ocel.op} on ${claims.ocel.ch}`;
    }
    return undefined;
  } catch {
    return "the token does not verify";
  }
}

function readConnectToken(protocols: Set<string>): string | undefined {
  for (const offered of protocols) {
    if (!offered.startsWith("header-")) continue;
    const header = JSON.parse(Buffer.from(offered.slice(7), "base64url").toString("utf8"));
    return header.Authorization;
  }
  return undefined;
}

function isSubscribedTo(subscribed: string, channel: string): boolean {
  if (subscribed === channel) return true;
  return subscribed.endsWith("/*") && channel.startsWith(subscribed.slice(0, -1));
}

/**
 * Serves a fake gateway's sockets, and points the SDK at a fake ocel runtime that publishes
 * to it, answering each publish with `status`.
 */
export async function serveFakeGateway({
  status = 202,
  acknowledgingConnections = true,
  answeringSubscribes = true,
  connectTokenLifetimeMs,
  pingIntervalMs,
  pongTimeoutMs = 1_000,
}: FakeGatewayOptions = {}): Promise<FakeGateway> {
  const published: PublishedEvent[] = [];
  const sockets: WebSocket[] = [];
  const connectTokens: string[] = [];
  const closedForPolicy: string[] = [];
  const heldTokens = new Set<string>();
  const subscribes: { id: string; channel: string }[] = [];
  const live = new Map<WebSocket, Map<string, string>>();
  const relay = (channel: string, envelope: string) => {
    for (const [socket, subscriptions] of live) {
      for (const [id, subscribed] of subscriptions) {
        if (isSubscribedTo(subscribed, channel)) {
          socket.send(JSON.stringify({ type: "data", id, event: envelope }));
        }
      }
    }
  };
  const server = createServer((_req, res) => {
    res.statusCode = 404;
    res.end();
  });
  const publishes: GatewayPublishes = { published, status, relay };
  publishingGateway = publishes;
  const runtime = await serveFakeRuntime();
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address() as AddressInfo;
  const host = `127.0.0.1:${port}`;
  const url = `ws://${host}${socketPath}`;
  const socketServer = new WebSocketServer({
    server,
    path: socketPath,
    handleProtocols: (protocols) => (protocols.has(subprotocol) ? subprotocol : false),
  });
  socketServer.on("connection", (socket, req) => {
    sockets.push(socket);
    const offered = new Set(
      (req.headers["sec-websocket-protocol"] ?? "").split(",").map((protocol) => protocol.trim()),
    );
    const connectToken = readConnectToken(offered) ?? "";
    connectTokens.push(connectToken);
    const refusal = heldTokens.has(connectToken)
      ? "the connect token already holds its limit of 1 socket; ask the app's realtime handler for another"
      : findRefusal(connectToken, host, "connect", "/app");
    if (refusal) {
      socket.send(
        JSON.stringify({
          type: "connection_error",
          errors: [{ errorType: "UnauthorizedException", message: refusal }],
        }),
      );
      socket.close(1008, "unauthorized");
      return;
    }
    heldTokens.add(connectToken);
    const closeForPolicy = (reason: string) => {
      closedForPolicy.push(reason);
      socket.close(1008, reason);
    };
    const expiresInMs =
      connectTokenLifetimeMs ?? Number(readClaims(connectToken).exp) * 1_000 - Date.now();
    const expiry = setTimeout(
      () => closeForPolicy("the connection's token has expired"),
      expiresInMs,
    );
    let pongDeadline: ReturnType<typeof setTimeout> | undefined;
    const pinging =
      pingIntervalMs === undefined
        ? undefined
        : setInterval(() => {
            socket.ping();
            pongDeadline ??= setTimeout(() => socket.terminate(), pongTimeoutMs);
          }, pingIntervalMs);
    socket.on("pong", () => {
      clearTimeout(pongDeadline);
      pongDeadline = undefined;
    });
    const subscriptions = new Map<string, string>();
    live.set(socket, subscriptions);
    socket.on("close", () => {
      live.delete(socket);
      heldTokens.delete(connectToken);
      clearTimeout(expiry);
      clearTimeout(pongDeadline);
      clearInterval(pinging);
    });
    socket.on("message", (raw) => {
      const frame = JSON.parse(raw.toString());
      const reply = (answer: unknown) => socket.send(JSON.stringify(answer));
      if (frame.type === "connection_init") {
        if (!acknowledgingConnections) return;
        reply({ type: "connection_ack", connectionTimeoutMs: 300_000 });
      } else if (frame.type === "subscribe") {
        subscribes.push({ id: frame.id, channel: frame.channel });
        if (!answeringSubscribes) return;
        const denied = findRefusal(
          frame.authorization?.Authorization,
          host,
          "subscribe",
          frame.channel,
        );
        if (denied) {
          reply({
            type: "subscribe_error",
            id: frame.id,
            errors: [{ errorType: "UnauthorizedException", message: denied }],
          });
          return;
        }
        subscriptions.set(frame.id, frame.channel);
        reply({ type: "subscribe_success", id: frame.id });
      } else if (frame.type === "unsubscribe") {
        subscriptions.delete(frame.id);
        reply({ type: "unsubscribe_success", id: frame.id });
      }
    });
  });
  return {
    runtime,
    host,
    url,
    published,
    sockets,
    connectTokens,
    closedForPolicy,
    subscribes,
    dropSockets() {
      for (const socket of live.keys()) socket.terminate();
    },
    binding: JSON.stringify({
      name: "realtime--app",
      realtime: { url, host },
    }),
    close: () =>
      new Promise<void>((resolve, reject) => {
        if (publishingGateway === publishes) publishingGateway = undefined;
        for (const socket of sockets) socket.terminate();
        socketServer.close();
        server.close((err) => (err ? reject(err) : resolve()));
        server.closeAllConnections();
      }),
  };
}
