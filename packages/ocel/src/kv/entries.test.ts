import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { BindingType } from "../gen/proto/common/bindings/v1/bindings_pb.js";
import { readKvFixture } from "../testing/kv-fixture.js";
import { bindingKey } from "../utils/get-config.js";

vi.mock("../utils/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
}));

vi.mock("ioredis", async () => {
  const { ValkeyMock } = await import("../testing/valkey-mock.js");
  return { Redis: ValkeyMock, default: ValkeyMock };
});

const { kv, InvalidKVValueError } = await import("./index.js");

const Session = z.object({ user: z.string(), visits: z.number().int() });

let port = 8000;

function deliver(name: string) {
  port += 1;
  vi.stubEnv(
    bindingKey(name, BindingType.KV),
    JSON.stringify({ name: `kv--${name}`, kv: { host: "127.0.0.1", port, password: "pw" } }),
  );
}

beforeEach(() => {
  vi.stubEnv("OCEL_PHASE", "");
});

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("a text entry", () => {
  it("reads what was written, and misses as undefined", async () => {
    deliver("text");
    const store = kv("text", {
      entries: { greeting: kv.text("greeting"), note: kv.text("notes/:id") },
    });

    expect(await store.greeting.get()).toBeUndefined();
    await store.greeting.set("hello");
    await store.note.set({ id: "a" }, "first");
    expect(await store.greeting.get()).toBe("hello");
    expect(await store.note.getMany([{ id: "a" }, { id: "b" }])).toEqual(["first", undefined]);
    expect(await store.note.delete({ id: "a" })).toBe(true);
    expect(await store.note.delete({ id: "a" })).toBe(false);
    expect(await store.client.get("greeting")).toBe("hello");
  });
});

describe("a counter entry", () => {
  it("counts atomically from zero, up and down", async () => {
    deliver("counter");
    const store = kv("counter", { entries: { hits: kv.counter("hits/:page") } });

    expect(await store.hits.get({ page: "home" })).toBeUndefined();
    expect(await store.hits.increment({ page: "home" })).toBe(1);
    expect(await store.hits.increment({ page: "home" }, 5)).toBe(6);
    expect(await store.hits.decrement({ page: "home" }, 2)).toBe(4);
    expect(await store.hits.decrement({ page: "other" })).toBe(-1);
    await store.hits.set({ page: "set" }, 10);
    expect(await store.hits.getMany([{ page: "home" }, { page: "set" }, { page: "none" }])).toEqual(
      [4, 10, undefined],
    );
  });

  it("refuses a stored value that is no integer", async () => {
    deliver("bad-counter");
    const store = kv("bad-counter", { entries: { hits: kv.counter("hits") } });
    await store.client.set("hits", "lots");

    await expect(store.hits.get()).rejects.toThrow(InvalidKVValueError);
  });

  it.each(["007", "-0", "+1", "1e3", " 1"])(
    "refuses %j, which Valkey would not count from",
    async (stored) => {
      deliver("loose-counter");
      const store = kv("loose-counter", { entries: { hits: kv.counter("hits") } });
      await store.client.set("hits", stored);

      await expect(store.hits.get()).rejects.toThrow(InvalidKVValueError);
    },
  );

  it("refuses to read a count past the integers a number holds exactly", async () => {
    deliver("huge-counter");
    const store = kv("huge-counter", { entries: { hits: kv.counter("hits") } });
    await store.client.set("hits", "9007199254740993");

    await expect(store.hits.get()).rejects.toThrow(RangeError);
    await expect(store.hits.getMany([])).resolves.toEqual([]);
    await expect(store.hits.getMany([{}] as never)).rejects.toThrow(/9007199254740993/);
  });

  it("refuses to answer a sum past the integers a number holds exactly", async () => {
    deliver("overflowing-counter");
    const store = kv("overflowing-counter", { entries: { hits: kv.counter("hits") } });
    await store.hits.set(Number.MAX_SAFE_INTEGER);

    await expect(store.hits.increment()).rejects.toThrow(RangeError);
    await store.hits.set(Number.MIN_SAFE_INTEGER);
    await expect(store.hits.decrement()).rejects.toThrow(RangeError);
  });
});

describe("a json entry", () => {
  it("checks a value against its schema on write and on read", async () => {
    deliver("json");
    const store = kv("json", {
      entries: { session: kv.json("session/:id", { schema: Session }) },
    });

    await store.session.set({ id: "s1" }, { user: "ada", visits: 2 });
    expect(await store.session.get({ id: "s1" })).toEqual({ user: "ada", visits: 2 });
    expect(await store.session.get({ id: "none" })).toBeUndefined();

    await expect(store.session.set({ id: "s2" }, { user: "ada", visits: 1.5 })).rejects.toThrow(
      InvalidKVValueError,
    );
    expect(await store.client.exists("session/s2")).toBe(0);

    await store.client.set("session/s3", JSON.stringify({ user: 7 }));
    await store.client.set("session/s4", "not json");
    await expect(store.session.get({ id: "s3" })).rejects.toThrow(/fails its schema/);
    await expect(store.session.get({ id: "s4" })).rejects.toThrow(/no JSON/);
  });

  it("stores the value its schema answers, not the value it was given", async () => {
    deliver("json-output");
    const Trimmed = z.object({ user: z.string().trim(), visits: z.number().default(0) });
    const store = kv("json-output", {
      entries: { session: kv.json("session/:id", { schema: Trimmed }) },
    });

    await store.session.set({ id: "s1" }, { user: "  ada ", extra: true } as never);

    expect(await store.client.get("session/s1")).toBe('{"user":"ada","visits":0}');
  });

  it("reads an invalid stored value as a miss when declared onInvalid miss", async () => {
    deliver("lenient");
    const store = kv("lenient", {
      entries: { session: kv.json("session/:id", { schema: Session, onInvalid: "miss" }) },
    });
    await store.client.set("session/s3", JSON.stringify({ user: 7 }));
    await store.client.set("session/s4", "not json");

    expect(await store.session.get({ id: "s3" })).toBeUndefined();
    expect(await store.session.getMany([{ id: "s3" }, { id: "s4" }])).toEqual([
      undefined,
      undefined,
    ]);
  });
});

describe("a list entry", () => {
  it("keeps its values in order, through Array verbs", async () => {
    deliver("list");
    const store = kv("list", { entries: { recent: kv.list("recent/:user") } });
    const key = { user: "ada" };

    expect(await store.recent.push(key, ["b", "c"])).toBe(2);
    expect(await store.recent.push(key, "d")).toBe(3);
    expect(await store.recent.unshift(key, ["z", "a"])).toBe(5);
    expect(await store.recent.slice(key)).toEqual(["z", "a", "b", "c", "d"]);
    expect(await store.recent.slice(key, 1, 3)).toEqual(["a", "b"]);
    expect(await store.recent.slice(key, -2)).toEqual(["c", "d"]);
    expect(await store.recent.slice(key, 0, -1)).toEqual(["z", "a", "b", "c"]);
    expect(await store.recent.slice(key, 0, 0)).toEqual([]);
    expect(await store.recent.at(key, 0)).toBe("z");
    expect(await store.recent.at(key, -1)).toBe("d");
    expect(await store.recent.at(key, 9)).toBeUndefined();
    expect(await store.recent.pop(key)).toBe("d");
    expect(await store.recent.shift(key)).toBe("z");
    expect(await store.recent.length(key)).toBe(3);
    expect(await store.recent.delete(key)).toBe(true);
    expect(await store.recent.pop(key)).toBeUndefined();
  });
});

describe("a set entry", () => {
  it("keeps distinct members, through Set verbs", async () => {
    deliver("set");
    const store = kv("set", { entries: { online: kv.set("online/:room") } });
    const room = { room: "lobby" };

    expect(await store.online.add(room, ["ada", "bob"])).toBe(2);
    expect(await store.online.add(room, "ada")).toBe(0);
    expect(await store.online.has(room, "ada")).toBe(true);
    expect(await store.online.size(room)).toBe(2);
    expect(await store.online.values(room)).toEqual(new Set(["ada", "bob"]));
    expect(await store.online.delete(room, "ada")).toBe(true);
    expect(await store.online.delete(room, "ada")).toBe(false);
    expect(await store.online.has(room, "ada")).toBe(false);
    expect(await store.online.size(room)).toBe(1);
    expect(await store.online.clear(room)).toBe(true);
    expect(await store.online.size(room)).toBe(0);
  });
});

describe("an entry's ttl", () => {
  function store() {
    deliver("ttl");
    return kv("ttl", {
      entries: {
        session: kv.text("session/:id", { ttl: "30d" }),
        forever: kv.text("forever/:id"),
        hits: kv.counter("hits/:id", { ttl: "10s" }),
        recent: kv.list("recent/:id", { ttl: "1h" }),
        online: kv.set("online/:id", { ttl: "5m" }),
      },
    });
  }

  async function secondsLeft(cache: ReturnType<typeof store>, key: string) {
    const milliseconds = await cache.client.pttl(key);
    return milliseconds < 0 ? milliseconds : Math.ceil(milliseconds / 1_000);
  }

  it("is applied with every write of an entry that declares one", async () => {
    const cache = store();

    await cache.session.set({ id: "a" }, "x");
    await cache.hits.increment({ id: "a" });
    await cache.recent.push({ id: "a" }, "x");
    await cache.online.add({ id: "a" }, "x");

    expect(await secondsLeft(cache, "session/a")).toBe(30 * 86_400);
    expect(await secondsLeft(cache, "hits/a")).toBe(10);
    expect(await secondsLeft(cache, "recent/a")).toBe(3_600);
    expect(await secondsLeft(cache, "online/a")).toBe(300);
  });

  it("is overridden, kept or cleared by a write that says so", async () => {
    const cache = store();

    await cache.session.set({ id: "o" }, "x", { ttl: "5s" });
    expect(await secondsLeft(cache, "session/o")).toBe(5);

    await cache.session.set({ id: "o" }, "y", { ttl: "keep" });
    expect(await secondsLeft(cache, "session/o")).toBe(5);

    await cache.session.set({ id: "o" }, "z", { ttl: null });
    expect(await secondsLeft(cache, "session/o")).toBe(-1);

    await cache.hits.increment({ id: "o" }, 1, { ttl: "1m" });
    await cache.hits.increment({ id: "o" }, 1, { ttl: "keep" });
    expect(await secondsLeft(cache, "hits/o")).toBe(60);
    await cache.hits.decrement({ id: "o" }, 1, { ttl: null });
    expect(await secondsLeft(cache, "hits/o")).toBe(-1);
  });

  it("is cleared by a write to an entry that declares none", async () => {
    const cache = store();
    await cache.client.set("forever/a", "x", "PX", 1_000);

    await cache.forever.set({ id: "a" }, "y");

    expect(await secondsLeft(cache, "forever/a")).toBe(-1);
  });
});

describe("the kv value fixture, which every SDK reads and writes alike", () => {
  const fixture = readKvFixture();
  const Fixed = z.object({
    user: z.string(),
    roles: z.array(z.string()),
    visits: z.number(),
    note: z.string(),
  });

  function store() {
    deliver("encoding");
    return kv("encoding", {
      entries: {
        text: kv.text("text"),
        counter: kv.counter("counter"),
        json: kv.json("json", { schema: Fixed }),
        list: kv.list("list"),
        set: kv.set("set"),
      },
    });
  }

  it("stores text, counters and json as the fixture does, and reads the fixture's back", async () => {
    const cache = store();
    for (const { value, stored } of fixture.values.text) {
      await cache.text.set(value);
      expect(await cache.client.get("text")).toBe(stored);
      await cache.client.set("text", stored);
      expect(await cache.text.get()).toBe(value);
    }
    for (const { value, stored } of fixture.values.counter) {
      await cache.counter.set(value);
      expect(await cache.client.get("counter")).toBe(stored);
      await cache.client.set("counter", stored);
      expect(await cache.counter.get()).toBe(value);
    }
    for (const { value, stored } of fixture.values.json) {
      await cache.json.set(value as z.input<typeof Fixed>);
      expect(await cache.client.get("json")).toBe(stored);
      await cache.client.set("json", stored);
      expect(await cache.json.get()).toEqual(value);
    }
  });

  it("stores lists and sets as the fixture does, and reads the fixture's back", async () => {
    const cache = store();
    for (const { value, stored } of fixture.values.list) {
      await cache.list.push(value);
      expect(await cache.client.lrange("list", 0, -1)).toEqual(stored);
      await cache.client.del("list");
      await cache.client.rpush("list", ...stored);
      expect(await cache.list.slice()).toEqual(value);
    }
    for (const { value, stored } of fixture.values.set) {
      await cache.set.add(value);
      expect((await cache.client.smembers("set")).sort()).toEqual(stored);
      await cache.client.del("set");
      await cache.client.sadd("set", ...stored);
      expect(await cache.set.values()).toEqual(new Set(value));
    }
  });
});
