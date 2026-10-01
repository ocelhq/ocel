import { readFileSync } from "node:fs";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { bindingKey } from "../binding/binding.js";
import { KvShape, ResourceType } from "../gen/proto/app/resources/v1/resources_pb.js";
import { BindingType } from "../gen/proto/common/bindings/v1/bindings_pb.js";

const declareMock = vi.hoisted(() => vi.fn((_req: unknown) => Promise.resolve({})));

vi.mock("../runtime/rpc", () => ({
  rpc: { resource: { declare: declareMock } },
}));

vi.mock("ioredis", async () => {
  const { ValkeyMock } = await import("../testing/valkey-mock.js");
  return { Redis: ValkeyMock, default: ValkeyMock };
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
    bindingKey(name, BindingType.KV),
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

  it("refuses two entries whose patterns overlap, naming both and where each was declared", () => {
    expect(() =>
      kv("overlapping", {
        entries: {
          session: kv.text("session/:id"),
          current: kv.text("session/current"),
        },
      }),
    ).toThrow(/"current".*"session\/current".*overlaps.*"session\/:id" of entry "session"/);
    expect(declareMock).not.toHaveBeenCalled();
  });

  it("refuses a malformed pattern, saying what is wrong with it", () => {
    expect(() => kv("braces", { entries: { session: kv.text("session/{id}") } })).toThrow(
      /hash tag/,
    );
    expect(() => kv("twice", { entries: { pair: kv.text("a/:id/:id") } })).toThrow(/twice/);
    expect(() => kv("empty", { entries: { trailing: kv.text("a/") } })).toThrow(/empty segment/);
  });

  it.each(["client", "connectionString", "connection_string", "then", "constructor"])(
    "refuses an entry named %s, a member a store has in some SDK",
    (name) => {
      expect(() => kv("reserved", { entries: { [name]: kv.text("a/:id") } as never })).toThrow(
        `kv("reserved"): entry name "${name}" is reserved, since a store already has a member of that name in some SDK: name the entry otherwise`,
      );
      expect(declareMock).not.toHaveBeenCalled();
    },
  );

  it.each(["_session", "1st", "rate-limit", "café", "", "a".repeat(64)])(
    "refuses an entry named %j, which some SDK cannot hold as a name",
    (name) => {
      expect(() => kv("ungrammatical", { entries: { [name]: kv.text("a") } })).toThrow(
        `kv("ungrammatical"): entry name "${name}" is no name every SDK can hold: it starts with a letter and goes on in letters, digits and _, at most 63 characters`,
      );
      expect(declareMock).not.toHaveBeenCalled();
    },
  );

  it("takes an entry name of a letter and then up to 62 letters, digits and _", () => {
    const name = `a${"_9Z".repeat(20)}xy`;
    expect(name).toHaveLength(63);

    expect(() => kv("grammatical", { entries: { [name]: kv.text("a") } })).not.toThrow();
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
