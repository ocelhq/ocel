import type { PurgeMode } from "@platform/edge-contract/purge";
import { beforeEach, describe, expect, it } from "vitest";
import {
  PurgeBacklog,
  type PurgeOutcome,
  type PurgePort,
  type RaisedTag,
} from "../src/purge-backlog";
import { type MemoryBacklogStore, memoryBacklogStore } from "./purge-backlog-store";

const START = 1_700_000_000_000;

class FakeServe implements PurgePort {
  calls: { mode: PurgeMode | "everything"; tags: string[] }[] = [];
  refusals: PurgeOutcome[] = [];
  whileCalled: () => void = () => {};

  async purgeTags(mode: PurgeMode, tags: string[]): Promise<PurgeOutcome> {
    this.calls.push({ mode, tags });
    this.whileCalled();
    return this.refusals.shift() ?? { ok: true };
  }

  async purgeEverything(): Promise<PurgeOutcome> {
    this.calls.push({ mode: "everything", tags: [] });
    this.whileCalled();
    return this.refusals.shift() ?? { ok: true };
  }
}

const rateLimited: PurgeOutcome = { ok: false, codes: [1134], message: "rate limit exceeded" };

let now = START;
let serve: FakeServe;
let store: MemoryBacklogStore;
let backlog: PurgeBacklog;

const raised = (tag: string, raisedAt = now): RaisedTag => ({ tag, raisedAt });

function newBacklog(random: () => number): PurgeBacklog {
  return new PurgeBacklog({ store, serve, now: () => now, random, warn: () => {} });
}

beforeEach(() => {
  now = START;
  serve = new FakeServe();
  store = memoryBacklogStore();
  backlog = newBacklog(() => 1);
});

describe("the purge backlog coalescing raises", () => {
  it("purges every pending tag in one call", async () => {
    backlog.raise("purge", [raised("r1|a"), raised("r1|b"), raised("r1|c")]);

    await backlog.flush();

    expect(serve.calls).toEqual([{ mode: "purge", tags: ["r1|a", "r1|b", "r1|c"] }]);
  });

  it("sends at most 100 tags a call and the rest on the next flushes", async () => {
    const tags = Array.from({ length: 250 }, (_, i) => raised(`r1|t${String(i).padStart(3, "0")}`));
    backlog.raise("purge", tags);

    await backlog.flush();
    await backlog.flush();
    await backlog.flush();

    expect(serve.calls.map((call) => call.tags.length)).toEqual([100, 100, 50]);
    expect(serve.calls.flatMap((call) => call.tags)).toEqual(tags.map((t) => t.tag));
  });

  it("purges before it invalidates, one mode a call", async () => {
    backlog.raise("invalidate", [raised("r1|stale")]);
    backlog.raise("purge", [raised("r1|gone")]);

    await backlog.flush();
    await backlog.flush();

    expect(serve.calls).toEqual([
      { mode: "purge", tags: ["r1|gone"] },
      { mode: "invalidate", tags: ["r1|stale"] },
    ]);
  });

  it("lets a purge cover a pending invalidate of the same tag", async () => {
    backlog.raise("invalidate", [raised("r1|a")]);
    backlog.raise("purge", [raised("r1|a")]);

    await backlog.flush();
    await backlog.flush();

    expect(serve.calls).toEqual([{ mode: "purge", tags: ["r1|a"] }]);
  });

  it("keeps a pending purge when an invalidate of the same tag arrives", async () => {
    backlog.raise("purge", [raised("r1|a")]);
    backlog.raise("invalidate", [raised("r1|a")]);

    await backlog.flush();
    await backlog.flush();

    expect(serve.calls).toEqual([{ mode: "purge", tags: ["r1|a"] }]);
  });

  it("sends a tag raised twice once", async () => {
    backlog.raise("purge", [raised("r1|a")]);
    backlog.raise("purge", [raised("r1|a")]);

    await backlog.flush();

    expect(serve.calls).toEqual([{ mode: "purge", tags: ["r1|a"] }]);
  });

  it("does nothing when nothing is pending", async () => {
    const next = await backlog.flush();

    expect(serve.calls).toEqual([]);
    expect(next).toBeUndefined();
  });
});

describe("the purge backlog de-duplicating against what it purged", () => {
  it("drops a raise whose tag was purged at or after its raisedAt", async () => {
    backlog.raise("purge", [raised("r1|a")]);
    await backlog.flush();
    serve.calls = [];

    now += 5_000;
    backlog.raise("purge", [raised("r1|a", START - 1), raised("r1|a", START)]);
    await backlog.flush();

    expect(serve.calls).toEqual([]);
  });

  it("drops a raise made at the very moment its tag was purged", async () => {
    backlog.raise("purge", [raised("r1|a")]);
    await backlog.flush();
    serve.calls = [];

    backlog.raise("purge", [raised("r1|a", now)]);
    await backlog.flush();

    expect(serve.calls).toEqual([]);
  });

  it("keeps a raise made after the tag was last purged", async () => {
    backlog.raise("purge", [raised("r1|a")]);
    await backlog.flush();
    serve.calls = [];

    now += 5_000;
    backlog.raise("purge", [raised("r1|a", now)]);
    await backlog.flush();

    expect(serve.calls).toEqual([{ mode: "purge", tags: ["r1|a"] }]);
  });

  it("drops an invalidate covered by an earlier purge of the same tag", async () => {
    backlog.raise("purge", [raised("r1|a")]);
    await backlog.flush();
    serve.calls = [];

    backlog.raise("invalidate", [raised("r1|a", START)]);
    await backlog.flush();

    expect(serve.calls).toEqual([]);
  });

  it("does not let an invalidate stand in for a later purge of the same tag", async () => {
    backlog.raise("invalidate", [raised("r1|a")]);
    await backlog.flush();
    serve.calls = [];

    backlog.raise("purge", [raised("r1|a", START)]);
    await backlog.flush();

    expect(serve.calls).toEqual([{ mode: "purge", tags: ["r1|a"] }]);
  });

  it("keeps a raise that arrives while the purge it races is in flight", async () => {
    backlog.raise("purge", [raised("r1|a")]);
    const inFlight = backlog.flush();
    now += 10;
    backlog.raise("purge", [raised("r1|a", now)]);
    await inFlight;
    serve.calls = [];

    await backlog.flush();

    expect(serve.calls).toEqual([{ mode: "purge", tags: ["r1|a"] }]);
  });

  it("keeps a purge that arrives while an invalidate of its tag is in flight", async () => {
    backlog.raise("invalidate", [raised("r1|a")]);
    const inFlight = backlog.flush();
    now += 10;
    backlog.raise("purge", [raised("r1|a", now)]);
    await inFlight;
    serve.calls = [];

    await backlog.flush();

    expect(serve.calls).toEqual([{ mode: "purge", tags: ["r1|a"] }]);
  });

  it("forgets what it purged after an hour so its memory stays bounded", async () => {
    backlog.raise("purge", [raised("r1|a"), raised("r1|b")]);
    await backlog.flush();
    expect(store.purgedTags()).toEqual(["r1|a", "r1|b"]);

    now += 3_600_001;
    backlog.raise("purge", [raised("r1|c")]);
    await backlog.flush();

    expect(store.purgedTags()).toEqual(["r1|c"]);
  });
});

describe("the purge backlog backing off error 1134", () => {
  it("keeps the tags pending and does not call again before the wait ends", async () => {
    serve.refusals.push(rateLimited);
    backlog.raise("purge", [raised("r1|a")]);

    const next = await backlog.flush();
    now += 500;
    await backlog.flush();

    expect(next).toBeGreaterThan(now);
    expect(serve.calls).toHaveLength(1);
  });

  it("recovers: the first flush after the waits purges the same tags", async () => {
    serve.refusals.push(rateLimited, rateLimited);
    backlog.raise("purge", [raised("r1|a")]);

    now = (await backlog.flush())!;
    now = (await backlog.flush())!;
    const next = await backlog.flush();

    expect(serve.calls).toEqual([
      { mode: "purge", tags: ["r1|a"] },
      { mode: "purge", tags: ["r1|a"] },
      { mode: "purge", tags: ["r1|a"] },
    ]);
    expect(next).toBeUndefined();
  });

  it("doubles the wait on each refusal up to a ceiling", async () => {
    backlog.raise("purge", [raised("r1|a", START + 10 * 3_600_000)]);
    now = START + 10 * 3_600_000;
    const waits: number[] = [];
    for (let i = 0; i < 10; i++) {
      serve.refusals.push(rateLimited);
      const next = (await backlog.flush())!;
      waits.push(next - now);
      now = next;
    }

    expect(waits.slice(0, 4)).toEqual([2_000, 4_000, 8_000, 16_000]);
    expect(Math.max(...waits)).toBe(30_000);
    expect(waits.at(-1)).toBe(30_000);
  });

  it("starts the wait over once a call succeeds", async () => {
    backlog.raise("purge", [raised("r1|a")]);
    serve.refusals.push(rateLimited, rateLimited);
    now = (await backlog.flush())!;
    now = (await backlog.flush())!;
    await backlog.flush();

    backlog.raise("purge", [raised("r1|b", now)]);
    serve.refusals.push(rateLimited);
    const next = await backlog.flush();

    expect(next! - now).toBe(2_000);
  });

  it("backs off on any refusal, not only 1134", async () => {
    serve.refusals.push({ ok: false, codes: [], message: "internal error" });
    backlog.raise("purge", [raised("r1|a")]);

    const next = await backlog.flush();

    expect(next).toBeGreaterThan(now + 1_000);
  });

  it("jitters the wait down to half its ceiling", async () => {
    backlog = newBacklog(() => 0);
    serve.refusals.push(rateLimited, rateLimited);
    backlog.raise("purge", [raised("r1|a")]);

    now = (await backlog.flush())!;
    const next = await backlog.flush();

    expect(next! - now).toBe(2_000);
  });
});

describe("the purge backlog escalating after 60 seconds", () => {
  it("purges everything once the oldest pending raise is older than 60 seconds", async () => {
    backlog.raise("purge", [raised("r1|a", START)]);
    backlog.raise("purge", [raised("r1|b", START + 30_000)]);
    now = START + 60_001;

    await backlog.flush();

    expect(serve.calls).toEqual([{ mode: "everything", tags: [] }]);
  });

  it("does not escalate a backlog that is exactly 60 seconds old", async () => {
    backlog.raise("purge", [raised("r1|a", START)]);
    now = START + 60_000;

    await backlog.flush();

    expect(serve.calls.map((call) => call.mode)).toEqual(["purge"]);
  });

  it("drops the backlog after it escalates, so it escalates once", async () => {
    backlog.raise("purge", [raised("r1|a", START)]);
    backlog.raise("invalidate", [raised("r1|b", START)]);
    now = START + 61_000;

    await backlog.flush();
    now += 1_000;
    await backlog.flush();
    await backlog.flush();

    expect(serve.calls).toEqual([{ mode: "everything", tags: [] }]);
  });

  it("drops a late duplicate of what the escalation cleared", async () => {
    backlog.raise("purge", [raised("r1|a", START)]);
    now = START + 61_000;
    await backlog.flush();

    backlog.raise("purge", [raised("r1|a", START + 1_000), raised("r1|other", START + 10_000)]);
    await backlog.flush();

    expect(serve.calls).toEqual([{ mode: "everything", tags: [] }]);
  });

  it("keeps a raise made after the escalation began", async () => {
    backlog.raise("purge", [raised("r1|a", START)]);
    now = START + 61_000;
    const escalating = backlog.flush();
    now += 10;
    backlog.raise("purge", [raised("r1|late", now)]);
    await escalating;

    await backlog.flush();

    expect(serve.calls).toEqual([
      { mode: "everything", tags: [] },
      { mode: "purge", tags: ["r1|late"] },
    ]);
  });

  it("tries again after the wait when purging everything is itself rate limited", async () => {
    backlog.raise("purge", [raised("r1|a", START)]);
    now = START + 61_000;
    serve.refusals.push(rateLimited);

    now = (await backlog.flush())!;
    await backlog.flush();
    const next = await backlog.flush();

    expect(serve.calls).toEqual([
      { mode: "everything", tags: [] },
      { mode: "everything", tags: [] },
    ]);
    expect(next).toBeUndefined();
  });

  it("does not escalate a tag raised again during every call that purges it", async () => {
    backlog.raise("purge", [raised("r1|hot")]);
    serve.whileCalled = () => {
      now += 10;
      backlog.raise("purge", [raised("r1|hot", now)]);
    };

    for (let second = 0; second < 70; second++) {
      now += 1_000;
      await backlog.flush();
    }

    expect(serve.calls.filter((call) => call.mode === "everything").length).toBeLessThanOrEqual(1);
  });

  it("escalates a backlog that backing off kept past 60 seconds", async () => {
    backlog.raise("purge", [raised("r1|a", START)]);
    serve.refusals.push(rateLimited);
    await backlog.flush();
    now = START + 90_000;
    serve.calls = [];

    await backlog.flush();

    expect(serve.calls).toEqual([{ mode: "everything", tags: [] }]);
  });
});

describe("the purge backlog scheduling its next flush", () => {
  it("asks for a flush a second after a raise, so raises that follow coalesce", () => {
    backlog.raise("purge", [raised("r1|a")]);

    expect(backlog.readNextFlushAt()).toBe(now + 1_000);
  });

  it("asks for nothing while nothing is pending", () => {
    expect(backlog.readNextFlushAt()).toBeUndefined();
  });

  it("asks for nothing when every raise was already covered", async () => {
    backlog.raise("purge", [raised("r1|a")]);
    await backlog.flush();

    backlog.raise("purge", [raised("r1|a", START)]);

    expect(backlog.readNextFlushAt()).toBeUndefined();
  });

  it("asks for another flush a second later when a call left tags behind", async () => {
    backlog.raise(
      "purge",
      Array.from({ length: 101 }, (_, i) => raised(`r1|t${i}`)),
    );

    const next = await backlog.flush();

    expect(next).toBe(now + 1_000);
  });
});
