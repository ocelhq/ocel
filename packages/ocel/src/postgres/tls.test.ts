import { once } from "node:events";
import { createServer, type Server, type Socket } from "node:net";
import type { PeerCertificate } from "node:tls";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PostgresTlsMode } from "../gen/proto/common/bindings/v1/bindings_pb.js";
import { tlsOptionsOf, tlsPolicyOf } from "./tls.js";

vi.mock("../runtime/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
}));

const { postgres } = await import("./index.js");

const sslRequest = Buffer.from([0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f]);
const tlsHandshakeRecord = 0x16;
const protocolVersion3 = Buffer.from([0, 3, 0, 0]);

interface Exchange {
  first: Buffer;
  second: Buffer;
}

let server: Server | undefined;

afterEach(async () => {
  vi.unstubAllEnvs();
  if (server) {
    server.close();
    server = undefined;
  }
});

beforeEach(() => {
  vi.stubEnv("OCEL_PHASE", "");
});

async function serve(
  answer: "S" | "N" | undefined,
): Promise<{ port: number; exchange: Promise<Exchange> }> {
  let settle: (exchange: Exchange) => void = () => {};
  const exchange = new Promise<Exchange>((resolve) => {
    settle = resolve;
  });
  server = createServer((socket: Socket) => {
    socket.once("data", (first: Buffer) => {
      if (answer === undefined) {
        settle({ first, second: Buffer.alloc(0) });
        socket.destroy();
        return;
      }
      socket.write(answer);
      socket.once("data", (second: Buffer) => {
        settle({ first, second });
        socket.destroy();
      });
    });
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("the fake server has no port");
  return { port: address.port, exchange };
}

function deliver(properties: Record<string, unknown>) {
  vi.stubEnv(
    "OCEL_RESOURCE_POSTGRES_orders",
    JSON.stringify({ name: "orders", postgres: properties }),
  );
}

function parts(port: number, extra: Record<string, unknown> = {}) {
  return { host: "127.0.0.1", port, database: "d", username: "u", password: "p", ...extra };
}

describe("postgres() TLS negotiation", () => {
  it("asks for TLS before anything else when the record names no mode, and goes on in TLS when the server accepts", async () => {
    const { port, exchange } = await serve("S");
    deliver(parts(port));

    const pool = postgres("orders");
    pool.on("error", () => {});
    const connecting = pool.connect().catch(() => undefined);
    const { first, second } = await exchange;

    expect(first).toEqual(sslRequest);
    expect(second[0]).toBe(tlsHandshakeRecord);
    await connecting;
    await pool.end();
  });

  it("goes on in plaintext when the record names no mode and the server refuses TLS, as libpq's prefer does", async () => {
    const { port, exchange } = await serve("N");
    deliver(parts(port));

    const pool = postgres("orders");
    pool.on("error", () => {});
    const connecting = pool.connect().catch(() => undefined);
    const { first, second } = await exchange;

    expect(first).toEqual(sslRequest);
    expect(second.subarray(4, 8)).toEqual(protocolVersion3);
    await connecting;
    await pool.end();
  });

  it("refuses to go on in plaintext when the record requires TLS and the server refuses it", async () => {
    const { port } = await serve("N");
    deliver(parts(port, { tlsMode: "POSTGRES_TLS_MODE_REQUIRE" }));

    const pool = postgres("orders");
    pool.on("error", () => {});

    await expect(pool.connect()).rejects.toThrow(/does not accept TLS.*sslmode=require/);
    await pool.end();
  });

  it("asks for TLS when the record's url names no sslmode", async () => {
    const { port, exchange } = await serve("S");
    deliver({ url: `postgres://u:p@127.0.0.1:${port}/d` });

    const pool = postgres("orders");
    pool.on("error", () => {});
    const connecting = pool.connect().catch(() => undefined);
    const { first } = await exchange;

    expect(first).toEqual(sslRequest);
    await connecting;
    await pool.end();
  });

  it("sends no TLS request under a url's sslmode=disable", async () => {
    const { port, exchange } = await serve(undefined);
    deliver({ url: `postgres://u:p@127.0.0.1:${port}/d?sslmode=disable` });

    const pool = postgres("orders");
    pool.on("error", () => {});
    const connecting = pool.connect().catch(() => undefined);
    const { first } = await exchange;

    expect(first.subarray(4, 8)).toEqual(protocolVersion3);
    await connecting;
    await pool.end();
  });
});

describe("tlsOptionsOf", () => {
  const certificateFor = (name: string) =>
    ({ subject: { CN: name }, subjectaltname: `DNS:${name}` }) as PeerCertificate;

  it("trusts the record's CA and verifies its tls server name under a url's sslmode=verify-full", () => {
    const ca = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n";
    const policy = tlsPolicyOf({
      url: "postgres://u:p@127.0.0.1:41234/d?sslmode=verify-full",
      tlsCa: ca,
      tlsServerName: "orders.cluster.internal",
    } as never);

    const options = tlsOptionsOf(policy, "127.0.0.1");

    expect(options).toMatchObject({
      ca,
      rejectUnauthorized: true,
      servername: "orders.cluster.internal",
    });
    expect(
      options.checkServerIdentity?.("127.0.0.1", certificateFor("orders.cluster.internal")),
    ).toBeUndefined();
    expect(options.checkServerIdentity?.("127.0.0.1", certificateFor("127.0.0.1"))).toBeInstanceOf(
      Error,
    );
  });

  it("verifies the host the record names when it names no tls server name", () => {
    const policy = tlsPolicyOf({
      host: "db.internal",
      tlsMode: PostgresTlsMode.VERIFY_FULL,
      tlsServerName: "",
      tlsCa: "",
      url: "",
    } as never);

    const options = tlsOptionsOf(policy, "db.internal");

    expect(
      options.checkServerIdentity?.("db.internal", certificateFor("db.internal")),
    ).toBeUndefined();
    expect(
      options.checkServerIdentity?.("db.internal", certificateFor("other.internal")),
    ).toBeInstanceOf(Error);
  });

  it("encrypts without checking the certificate under require and when no mode is named", () => {
    for (const tlsMode of [PostgresTlsMode.REQUIRE, PostgresTlsMode.UNSPECIFIED]) {
      const policy = tlsPolicyOf({ tlsMode, tlsServerName: "", tlsCa: "", url: "" } as never);

      expect(tlsOptionsOf(policy, "db").rejectUnauthorized).toBe(false);
    }
  });
});
