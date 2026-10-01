import { createPublicKey, verify } from "node:crypto";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
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
  close(): Promise<void>;
}

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

export async function serveFakeGateway({
  status = 202,
  answering = true,
}: FakeGatewayOptions = {}): Promise<FakeGateway> {
  const published: PublishedEvent[] = [];
  const server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", (chunk: Buffer) => chunks.push(chunk));
    req.on("end", () => {
      published.push({
        path: req.url ?? "",
        authorization: req.headers.authorization,
        envelope: JSON.parse(Buffer.concat(chunks).toString("utf8")),
      });
      if (!answering) return;
      res.statusCode = status;
      res.end();
    });
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address() as AddressInfo;
  const host = `127.0.0.1:${port}`;
  const url = `ws://${host}/realtime`;
  return {
    host,
    url,
    published,
    binding: JSON.stringify({
      name: "realtime--app",
      realtime: { ...fixture.realtime, transport: "REALTIME_TRANSPORT_OCEL_GATEWAY", url, host },
    }),
    close: () =>
      new Promise<void>((resolve, reject) => {
        server.close((err) => (err ? reject(err) : resolve()));
        server.closeAllConnections();
      }),
  };
}
