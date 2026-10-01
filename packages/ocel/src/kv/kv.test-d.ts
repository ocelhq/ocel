import type { Redis } from "ioredis";
import { describe, expectTypeOf, it } from "vitest";
import { z } from "zod";
import { kv } from "./kv.js";

const Session = z.object({ user: z.string(), since: z.string().transform((s) => new Date(s)) });

const cache = kv("cache", {
  entries: {
    greeting: kv.text("greeting"),
    requests: kv.counter("requests/:userId", { ttl: "10s" }),
    session: kv.json("session/:id", { schema: Session, ttl: "30d" }),
    members: kv.set("rooms/:room/members/:member"),
    recent: kv.list("recent/:userId"),
  },
});

describe("a kv store's entries typed from their declarations", () => {
  it("takes each parameter of the pattern as the key", () => {
    expectTypeOf(cache.requests.increment).parameter(0).toEqualTypeOf<{
      userId: string | number;
    }>();
    expectTypeOf(cache.members.has)
      .parameter(0)
      .toEqualTypeOf<{ room: string | number; member: string | number }>();
  });

  it("takes no key for a pattern without parameters", () => {
    expectTypeOf(cache.greeting.get).parameters.toEqualTypeOf<[]>();
    expectTypeOf(cache.greeting.set).parameter(0).toEqualTypeOf<string>();
  });

  it("refuses a key that misses a parameter", () => {
    // @ts-expect-error the key needs userId
    cache.requests.increment({ id: "x" });
    // @ts-expect-error the key needs room as well as member
    cache.members.has({ member: "ada" }, "ada");
  });

  it("writes a json entry's schema input and reads its output", () => {
    expectTypeOf(cache.session.get).returns.resolves.toEqualTypeOf<
      { user: string; since: Date } | undefined
    >();
    expectTypeOf(cache.session.set).parameter(1).toEqualTypeOf<{ user: string; since: string }>();
  });

  it("answers a miss as undefined and a list's values as strings", () => {
    expectTypeOf(cache.requests.get).returns.resolves.toEqualTypeOf<number | undefined>();
    expectTypeOf(cache.recent.slice).returns.resolves.toEqualTypeOf<string[]>();
    expectTypeOf(cache.members.values).returns.resolves.toEqualTypeOf<Set<string>>();
  });

  it("removes a set's member with delete and the whole set with clear, as a Set does", () => {
    expectTypeOf(cache.members.delete).parameters.toEqualTypeOf<
      [key: { room: string | number; member: string | number }, member: string]
    >();
    expectTypeOf(cache.members.delete).returns.resolves.toEqualTypeOf<boolean>();
    expectTypeOf(cache.members.clear).parameters.toEqualTypeOf<
      [key: { room: string | number; member: string | number }]
    >();
    expectTypeOf(cache.members.clear).returns.resolves.toEqualTypeOf<boolean>();
    expectTypeOf(cache.recent.delete).returns.resolves.toEqualTypeOf<boolean>();
  });

  it("reaches the store's own client and connection string", () => {
    expectTypeOf(cache.client).toEqualTypeOf<Redis>();
    expectTypeOf(cache.connectionString).toEqualTypeOf<string>();
  });

  it("refuses a bare number as a duration", () => {
    // @ts-expect-error a ttl is written with its unit
    kv.text("x", { ttl: 10 });
    cache.requests.increment({ userId: 1 }, 1, { ttl: "keep" });
    cache.requests.increment({ userId: 1 }, 1, { ttl: null });
    // @ts-expect-error a write's ttl is written with its unit
    cache.requests.increment({ userId: 1 }, 1, { ttl: 5 });
  });

  it("refuses an entry named after a member the store already has", () => {
    // @ts-expect-error client is the store's own client
    kv("reserved", { entries: { client: kv.text("clients/:id") } });
    // @ts-expect-error connection_string is the Python store's connection string
    kv("reserved", { entries: { connection_string: kv.text("urls/:id") } });
  });

  it("requires a schema for a json entry", () => {
    // @ts-expect-error json takes a schema
    kv.json("session/:id", {});
  });
});
