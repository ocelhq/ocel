import { createPublicKey, verify } from "node:crypto";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { type WebSocket, WebSocketServer } from "ws";
import { readRealtimeBindingFixture } from "./realtime-vectors.js";

export interface PublishedEvent {
  path: string;
  authorization: string | undefined;
  envelope: { v: number; id: string; ch: string; ts: number; kind: string; data: unknown };
}

export interface FakeGateway {
  binding: string;
  host: string;
  url: string;
  published: PublishedEvent[];
  sockets: WebSocket[];
  subscribes: { id: string; channel: string }[];
  dropSockets(): void;
  close(): Promise<void>;
}

const socketPath = "/event/realtime";
const subprotocol = "aws-appsync-event-ws";

const fixture = JSON.parse(readRealtimeBindingFixture());

export const appsyncBinding = readRealtimeBindingFixture();
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
  answering?: boolean;
}

function admits(
  token: string | undefined,
  host: string,
  op: string,
  channel: string,
): string | undefined {
  try {
    const claims = readClaims(token ?? "");
    if (claims.aud !== host) return "the token is for another host";
    if (Number(claims.exp) <= Date.now() / 1_000) return "the token has expired";
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

export async function serveFakeGateway({
  status = 202,
  answering = true,
}: FakeGatewayOptions = {}): Promise<FakeGateway> {
  const published: PublishedEvent[] = [];
  const sockets: WebSocket[] = [];
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
  const server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", (chunk: Buffer) => chunks.push(chunk));
    req.on("end", () => {
      const text = Buffer.concat(chunks).toString("utf8");
      const event: PublishedEvent = {
        path: req.url ?? "",
        authorization: req.headers.authorization,
        envelope: JSON.parse(text),
      };
      published.push(event);
      if (!answering) return;
      res.statusCode = status;
      res.end();
      if (status < 300) relay(event.envelope.ch, text);
    });
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
  socketServer.on("connection", (socket, req) => {
    sockets.push(socket);
    const offered = new Set(
      (req.headers["sec-websocket-protocol"] ?? "").split(",").map((p) => p.trim()),
    );
    const refusal = admits(readConnectToken(offered), host, "connect", "/app");
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
    const subscriptions = new Map<string, string>();
    live.set(socket, subscriptions);
    socket.on("close", () => live.delete(socket));
    socket.on("message", (raw) => {
      const frame = JSON.parse(raw.toString());
      const reply = (answer: unknown) => socket.send(JSON.stringify(answer));
      if (frame.type === "connection_init") {
        reply({ type: "connection_ack", connectionTimeoutMs: 300_000 });
      } else if (frame.type === "subscribe") {
        subscribes.push({ id: frame.id, channel: frame.channel });
        const denied = admits(frame.authorization?.Authorization, host, "subscribe", frame.channel);
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
    host,
    url,
    published,
    sockets,
    subscribes,
    dropSockets() {
      for (const socket of live.keys()) socket.terminate();
    },
    binding: JSON.stringify({
      name: "realtime--app",
      realtime: { ...fixture.realtime, transport: "REALTIME_TRANSPORT_OCEL_GATEWAY", url, host },
    }),
    close: () =>
      new Promise<void>((resolve, reject) => {
        for (const socket of sockets) socket.terminate();
        socketServer.close();
        server.close((err) => (err ? reject(err) : resolve()));
        server.closeAllConnections();
      }),
  };
}
