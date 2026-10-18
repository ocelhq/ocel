import { readFileSync } from "node:fs";
import type { PeerCertificate } from "node:tls";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { bindingKey } from "../binding/binding.js";
import { KvShape, ResourceType } from "../gen/proto/app/resources/v1/resources_pb.js";

const declareMock = vi.hoisted(() => vi.fn((_req: unknown) => Promise.resolve({})));

vi.mock("../runtime/rpc", () => ({
  rpc: { resource: { declare: declareMock } },
}));

vi.mock("node:module", async () => {
  const { ValkeyMock } = await import("../testing/valkey-mock.js");
  return { createRequire: () => () => ({ Redis: ValkeyMock }) };
});

const { kv, UnprovisionedResourceError } = await import("./index.js");

const Session = z.object({ user: z.string(), visits: z.number().int() });

let port = 7000;

function fixtureAuthority(): string {
  const fixture = new URL("../../../../proto/common/bindings/v1/fixtures/kv.json", import.meta.url);
  return JSON.parse(readFileSync(fixture, "utf8")).kv.caPem;
}

function deliver(name: string, properties: Record<string, unknown> = {}) {
  port += 1;
  vi.stubEnv(
    bindingKey(name, "kv"),
    JSON.stringify({
      name: `kv--${name}`,
      kv: { host: "127.0.0.1", port, username: "default", password: "pw", ...properties },
    }),
  );
}

describe("kv discovery declare", () => {
  beforeEach(() => {
    declareMock.mockClear();
  });

  it("declares a KV store with its options and every entry's name, pattern, shape and line", () => {
    kv("cache", {
      version: "8",
      eviction: "allkeys-lru",
      memory: "256mb",
      entries: {
        greeting: kv.text("greeting"),
        requests: kv.counter("requests/:userId", { ttl: "10s" }),
        session: kv.json("session/:id", { schema: Session, ttl: "30d" }),
        recent: kv.list("recent/:userId"),
        online: kv.set("online/:room"),
      },
    });

    expect(declareMock).toHaveBeenCalledWith({
      resource: { name: "cache", type: ResourceType.KV },
      config: {
        case: "kv",
        value: {
          version: "8",
          eviction: "allkeys-lru",
          memory: "256mb",
          entries: [
            {
              name: "greeting",
              pattern: "greeting",
              shape: KvShape.TEXT,
              source: expect.any(String),
            },
            {
              name: "requests",
              pattern: "requests/:userId",
              shape: KvShape.COUNTER,
              source: expect.any(String),
            },
            {
              name: "session",
              pattern: "session/:id",
              shape: KvShape.JSON,
              source: expect.any(String),
            },
            {
              name: "recent",
              pattern: "recent/:userId",
              shape: KvShape.LIST,
              source: expect.any(String),
            },
            {
              name: "online",
              pattern: "online/:room",
              shape: KvShape.SET,
              source: expect.any(String),
            },
          ],
        },
      },
      source: expect.any(String),
    });
  });

  it("leaves the version, eviction and memory to the provider when the store names none", () => {
    kv("plain");

    expect(declareMock).toHaveBeenCalledWith({
      resource: { name: "plain", type: ResourceType.KV },
      config: { case: "kv", value: { version: "", eviction: "", memory: "", entries: [] } },
      source: expect.any(String),
    });
  });

  it("refuses a ttl that is no duration", () => {
    expect(() => kv("ttl", { entries: { a: kv.text("a", { ttl: 10 as never }) } })).toThrow(
      /duration/,
    );
  });

  it("refuses every accessor while discovering, as postgres does", async () => {
    const cache = kv("cache", { entries: { requests: kv.counter("requests/:userId") } });

    expect(() => cache.client).toThrow(UnprovisionedResourceError);
    expect(() => cache.connectionString).toThrow(UnprovisionedResourceError);
    await expect(cache.requests.increment({ userId: "u" })).rejects.toThrow(
      UnprovisionedResourceError,
    );
  });
});

describe("kv at runtime", () => {
  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("connects one client per store to the delivered binding, and shares it after", () => {
    deliver("conn", { username: "app", password: "p@ss/word", tls: true });
    const cache = kv("conn");

    expect(cache.client).toBe(cache.client);
    expect(cache.client.options).toMatchObject({
      host: "127.0.0.1",
      port,
      username: "app",
      password: "p@ss/word",
      tls: {},
    });
    expect(cache.client.options.tls).not.toHaveProperty("servername");
    expect(cache.connectionString).toBe(`rediss://app:p%40ss%2Fword@127.0.0.1:${port}`);
  });

  it("names the store's host as the TLS server name when the host is a name", () => {
    deliver("named", { host: "cache.internal", tls: true });

    expect(kv("named").client.options.tls).toEqual({ servername: "cache.internal" });
  });

  it("names the tls server name a forwarded binding carries, not the forward it connects to", () => {
    deliver("forwarded", { tls: true, tlsServerName: "cache.internal" });

    expect(kv("forwarded").client.options).toMatchObject({
      host: "127.0.0.1",
      tls: { servername: "cache.internal" },
    });
  });

  it("verifies the certificate against a forwarded binding's tls server name when that name is an address", () => {
    deliver("forwardedByAddress", { tls: true, tlsServerName: "10.0.0.5" });

    const verify = kv("forwardedByAddress").client.options.tls?.checkServerIdentity;
    if (!verify)
      throw new Error("the client verifies the certificate against the forward, 127.0.0.1");
    const certificateFor = (address: string) =>
      ({ subject: {}, subjectaltname: `IP Address:${address}` }) as PeerCertificate;
    expect(verify("127.0.0.1", certificateFor("10.0.0.5"))).toBeUndefined();
    expect(verify("127.0.0.1", certificateFor("127.0.0.1"))).toBeInstanceOf(Error);
  });

  it("trusts only the certificate authority the binding delivers", () => {
    const ca = fixtureAuthority();
    deliver("private", { tls: true, caPem: ca });

    expect(kv("private").client.options.tls).toEqual({ ca });
  });

  it.each([
    ["no PEM block", "not a certificate"],
    [
      "a block that is not base64",
      "-----BEGIN CERTIFICATE-----\nfixture\n-----END CERTIFICATE-----\n",
    ],
    ["a private key", "-----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY-----\n"],
    ["an empty DER sequence", "-----BEGIN CERTIFICATE-----\nMAA=\n-----END CERTIFICATE-----\n"],
  ])("refuses a caPem holding %s, naming the key it arrived in", (_, caPem) => {
    deliver("garbled", { tls: true, caPem });

    expect(() => kv("garbled").client).toThrow(
      /OCEL_RESOURCE_KV_garbled delivers a caPem for its kv store that holds no PEM certificate/,
    );
  });

  it("names the store's URL redis:// when it takes no TLS", () => {
    deliver("plain-url", { username: "", password: "pw" });

    expect(kv("plain-url").connectionString).toBe(`redis://:pw@127.0.0.1:${port}`);
  });

  it("fails an operation naming the key no binding was delivered under", async () => {
    const cache = kv("undelivered", { entries: { hits: kv.counter("hits") } });

    await expect(cache.hits.get()).rejects.toThrow(/OCEL_RESOURCE_KV_undelivered/);
  });
});
