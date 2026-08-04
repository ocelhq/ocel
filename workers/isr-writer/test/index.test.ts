import { entryMissHeader, tagSnapshotKey, type TagSnapshot } from "@ocel/next-cache";
import { SELF, env, runDurableObjectAlarm, runInDurableObject } from "cloudflare:test";
import { afterEach, describe, expect, it, vi } from "vitest";

import { sha256Hex } from "@ocel/worker-auth";
import { IsrDeploy } from "../src/isr-deploy";
import { IsrSnapshot } from "../src/isr-snapshot";
import { claimBuild } from "../src/build";
import * as registry from "../src/registry";
import type { Env } from "../src/env";

declare module "cloudflare:test" {
  interface ProvidedEnv extends Env {}
}

const BOOTSTRAP = "dev-secret"; // matches wrangler.jsonc's vars.BOOTSTRAP_SECRET

function req(path: string, init: RequestInit = {}) {
  return new Request(`https://writer.example${path}`, init);
}

function bearerReq(path: string, token: string, init: RequestInit = {}) {
  return req(path, {
    ...init,
    headers: { ...init.headers, authorization: `Bearer ${token}` },
  });
}

// Each test gets its own isrPrefix so the worker's per-isolate hash memo — real,
// and shared across tests in one isolate — never carries a hash between them.
afterEach(() => {
  vi.useRealTimers();
});

let nextBuild = 0;
function freshPrefix() {
  return `prod/acme/web/BUILD${nextBuild++}`;
}

async function initialize(prefix: string, secret: string, token = BOOTSTRAP) {
  return SELF.fetch(
    bearerReq(`/${prefix}/initialize`, token, {
      method: "POST",
      body: JSON.stringify({ secretHash: await sha256Hex(secret) }),
    }),
  );
}

function writeEntryReq(prefix: string, key: string, secret: string, body = `{"value":1}`) {
  return SELF.fetch(
    bearerReq(`/${prefix}/entry?key=${encodeURIComponent(key)}`, secret, {
      method: "PUT",
      body,
    }),
  );
}

describe("initialize", () => {
  it("seeds a deploy's secret hash", async () => {
    const prefix = freshPrefix();
    expect((await initialize(prefix, "write-secret")).status).toBe(204);
  });

  it("rejects one signed with the wrong bootstrap credential", async () => {
    const res = await initialize(freshPrefix(), "write-secret", "wrong");
    expect(res.status).toBe(401);
  });

  it("rejects an unsigned one", async () => {
    const prefix = freshPrefix();
    const res = await SELF.fetch(
      req(`/${prefix}/initialize`, {
        method: "POST",
        body: JSON.stringify({ secretHash: await sha256Hex("s") }),
      }),
    );
    expect(res.status).toBe(401);
  });

  it("rejects a body that is not a secret hash", async () => {
    const prefix = freshPrefix();
    for (const secretHash of ["plaintext-secret", "a".repeat(63), undefined]) {
      const res = await SELF.fetch(
        bearerReq(`/${prefix}/initialize`, BOOTSTRAP, {
          method: "POST",
          body: JSON.stringify({ secretHash }),
        }),
      );
      expect(res.status).toBe(400);
    }
  });
});

describe("entry writes", () => {
  it("writes the entry into the deploy's own slice of the bucket", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");

    const res = await writeEntryReq(prefix, "blog/post", "write-secret", `{"value":7}`);
    expect(res.status).toBe(204);

    const stored = await env.OCEL_CACHE_STORE.get(`${prefix}/cache/blog/post.cache.json`);
    expect(await stored?.text()).toBe(`{"value":7}`);
  });

  it("rejects a write signed with the wrong secret", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");

    const res = await writeEntryReq(prefix, "blog/post", "other-secret");
    expect(res.status).toBe(401);
    expect(await env.OCEL_CACHE_STORE.get(`${prefix}/cache/blog/post.cache.json`)).toBeNull();
  });

  it("rejects a write signed with the bootstrap credential", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");

    expect((await writeEntryReq(prefix, "blog/post", BOOTSTRAP)).status).toBe(401);
  });

  it("rejects a write against an uninitialized deploy", async () => {
    const res = await writeEntryReq(freshPrefix(), "blog/post", "write-secret");
    expect(res.status).toBe(401);
  });

  it("rejects a write against a deploy the caller is not the one holding", async () => {
    const mine = freshPrefix();
    const theirs = freshPrefix();
    await initialize(mine, "my-secret");
    await initialize(theirs, "their-secret");

    expect((await writeEntryReq(theirs, "blog/post", "my-secret")).status).toBe(401);
  });

  it("rejects a key that would climb out of the deploy's slice", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");

    expect((await writeEntryReq(prefix, "../escape", "write-secret")).status).toBe(400);
    expect((await writeEntryReq(prefix, "", "write-secret")).status).toBe(400);
  });

  // The key grammar is @ocel/next-cache's entryObjectKey, the same function the
  // Lambda derives its own key with. A route the writer refuses but its caller
  // accepts is a route that renders on every request and never caches, and the
  // refusal reaches nobody: the caller's throw is raised inside background().
  // `trailingSlash: true` produces exactly that key on every route.
  it("accepts a trailing-slash route key, which the direct write path accepted", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");

    expect((await writeEntryReq(prefix, "blog/", "write-secret", `{"value":9}`)).status).toBe(204);

    const stored = await env.OCEL_CACHE_STORE.get(`${prefix}/cache/blog/.cache.json`);
    expect(await stored?.text()).toBe(`{"value":9}`);
  });
});

// Entry reads route through the writer for the same reason writes do: with both
// on this side of the boundary the deployed function holds no standing R2
// credential at all, which is the whole credential-hygiene case for the worker
// (epic decision 6). Reads are the hot path — far more frequent than writes —
// so they lean on exactly the same per-isolate hash memo.
describe("entry reads", () => {
  function readEntryReq(prefix: string, key: string, secret: string) {
    return SELF.fetch(bearerReq(`/${prefix}/entry?key=${encodeURIComponent(key)}`, secret));
  }

  // The two halves derive the object key with one function (@ocel/next-cache's
  // entryObjectKey), so a key the writer accepts is a key the reader finds. A
  // read that resolved anywhere else is a permanent miss: the route re-renders
  // on every request and the entry it just wrote is never seen again.
  it("reads back exactly what a write of the same key stored", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");

    for (const key of ["blog/post", "index", "blog/"]) {
      expect((await writeEntryReq(prefix, key, "write-secret", `{"k":"${key}"}`)).status).toBe(204);

      const res = await readEntryReq(prefix, key, "write-secret");
      expect(res.status, key).toBe(200);
      expect(await res.text()).toBe(`{"k":"${key}"}`);
    }
  });

  // A 404 is both "no entry here" and "no such route", and the reader fails open
  // on either — so without a marker on the first, a writer URL pointing at
  // nothing at all reads as a cache that is merely always cold.
  it("marks an entry that was never written as a miss, as no other 404 is", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");

    const miss = await readEntryReq(prefix, "never-written", "write-secret");
    expect(miss.status).toBe(404);
    expect(miss.headers.get(entryMissHeader)).toBe("1");

    for (const path of [`/${prefix}/nonsense`, "/initialize"]) {
      const res = await SELF.fetch(bearerReq(path, "write-secret"));
      expect(res.status, path).toBe(404);
      expect(res.headers.get(entryMissHeader), path).toBeNull();
    }
  });

  it("rejects a read signed with the wrong secret, and one signed with none", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");
    await writeEntryReq(prefix, "blog/post", "write-secret");

    expect((await readEntryReq(prefix, "blog/post", "other-secret")).status).toBe(401);
    expect((await SELF.fetch(req(`/${prefix}/entry?key=blog%2Fpost`))).status).toBe(401);
  });

  it("rejects a read of another deploy's slice", async () => {
    const mine = freshPrefix();
    const theirs = freshPrefix();
    await initialize(mine, "my-secret");
    await initialize(theirs, "their-secret");
    await writeEntryReq(theirs, "blog/post", "their-secret", `{"theirs":1}`);

    expect((await readEntryReq(theirs, "blog/post", "my-secret")).status).toBe(401);
  });

  it("rejects a key that would climb out of the deploy's slice", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");

    expect((await readEntryReq(prefix, "../escape", "write-secret")).status).toBe(400);
    expect((await readEntryReq(prefix, "", "write-secret")).status).toBe(400);
  });

  // The memo is what keeps the serving path off a single-threaded Durable
  // Object. A DO round trip per read would put every cache hit behind one
  // object per deploy — the ceiling the epic's decision 6c exists to avoid.
  it("costs no Durable Object round trip per read once the memo is warm", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");
    await writeEntryReq(prefix, "blog/post", "write-secret", `{"value":1}`);

    const secretHash = vi.spyOn(IsrDeploy.prototype, "secretHash");
    for (let i = 0; i < 10; i++) {
      expect((await readEntryReq(prefix, "blog/post", "write-secret")).status).toBe(200);
    }
    expect(secretHash).not.toHaveBeenCalled();
    secretHash.mockRestore();
  });
});

describe("destroy", () => {
  it("retires the deploy so its secret no longer authorizes a write", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");
    expect((await writeEntryReq(prefix, "a", "write-secret")).status).toBe(204);

    const res = await SELF.fetch(
      bearerReq(`/${prefix}/destroy`, BOOTSTRAP, { method: "POST" }),
    );
    expect(res.status).toBe(204);

    expect((await writeEntryReq(prefix, "b", "write-secret")).status).toBe(401);
  });

  it("rejects one signed with anything but the bootstrap credential", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");

    const res = await SELF.fetch(
      bearerReq(`/${prefix}/destroy`, "write-secret", { method: "POST" }),
    );
    expect(res.status).toBe(401);
  });
});

describe("routing", () => {
  it("404s an unknown op and a path with no deploy prefix", async () => {
    expect((await SELF.fetch(req("/prod/acme/web/B/nonsense"))).status).toBe(404);
    expect((await SELF.fetch(req("/initialize", { method: "POST" }))).status).toBe(404);
  });

  // idFromName materializes a storage-backed Durable Object for whatever name it
  // is handed, and the entry op reaches it before any credential is checked — so
  // a path that is not a deploy prefix must never get that far, or an
  // unauthenticated caller can create Durable Objects in the account at will.
  it("404s a path that is not exactly a four-segment deploy prefix", async () => {
    const secretHash = vi.spyOn(IsrDeploy.prototype, "secretHash");
    const paths = [
      "/aaa/entry",
      "/prod/acme/entry",
      "/prod/acme/web/B1/extra/entry",
      "/prod/acme/web/../entry",
      "/prod/acme/web/./entry",
    ];
    for (const path of paths) {
      const res = await SELF.fetch(
        bearerReq(`${path}?key=x`, "anything", { method: "PUT", body: "{}" }),
      );
      expect(res.status, path).toBe(404);
    }
    expect(secretHash).not.toHaveBeenCalled();
    secretHash.mockRestore();
  });
});

// Seeds a deploy straight into the DO's storage, leaving this isolate's memo
// untouched — which is what every isolate but the one that served the
// initialize sees, and what a redeploy of the same build does to all of them.
async function seedBehindTheWorker(prefix: string, secret: string) {
  const stub = env.ISR_WRITER_DO.get(env.ISR_WRITER_DO.idFromName(prefix));
  const hash = await sha256Hex(secret);
  await runInDurableObject(stub, (_instance, ctx) =>
    registry.initialize(ctx.storage, hash),
  );
}

// The memo only spares the DO once it is filled. A cold isolate taking a herd
// on one deploy — the case this whole worker exists for — must not turn every
// request in it into its own round trip to a single-threaded object.
describe("concurrent registry reads", () => {
  it("costs one Durable Object round trip for a herd against a cold memo", async () => {
    const prefix = freshPrefix();
    await seedBehindTheWorker(prefix, "write-secret");
    const secretHash = vi.spyOn(IsrDeploy.prototype, "secretHash");

    const results = await Promise.all(
      Array.from({ length: 10 }, (_, i) => writeEntryReq(prefix, `k${i}`, "write-secret")),
    );
    for (const res of results) expect(res.status).toBe(204);
    expect(secretHash).toHaveBeenCalledTimes(1);
    secretHash.mockRestore();
  });

  // Coalescing must not cache the failure: a DO that was unreachable once has to
  // be reachable on the next request, not for the memo's lifetime.
  it("retries after a registry read that failed", async () => {
    const prefix = freshPrefix();
    await seedBehindTheWorker(prefix, "write-secret");
    const secretHash = vi
      .spyOn(IsrDeploy.prototype, "secretHash")
      .mockRejectedValueOnce(new Error("durable object unreachable"));

    await expect(writeEntryReq(prefix, "a", "write-secret")).rejects.toThrow();
    expect((await writeEntryReq(prefix, "b", "write-secret")).status).toBe(204);
    secretHash.mockRestore();
  });
});

// The entry op consults the registry before any credential is checked, so an
// unauthenticated caller picks the name of the object it reaches. Instantiating
// one is unavoidable; leaving durable storage behind under an attacker-chosen
// name is not, and prefixes are unbounded.
describe("unknown deploys", () => {
  async function storedTables(prefix: string) {
    const stub = env.ISR_WRITER_DO.get(env.ISR_WRITER_DO.idFromName(prefix));
    return runInDurableObject(stub, (_instance, ctx) =>
      ctx.storage.sql
        .exec(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'registry'`)
        .toArray(),
    );
  }

  it("writes no durable storage for a prefix nobody deployed", async () => {
    const prefix = freshPrefix();
    expect((await writeEntryReq(prefix, "a", "junk")).status).toBe(401);

    expect(await storedTables(prefix)).toEqual([]);
  });

  it("still stores a deploy that is initialized", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");

    expect(await storedTables(prefix)).toEqual([{ name: "registry" }]);
  });
});

describe("secret rotation", () => {
  it("accepts the reseeded secret and refuses the superseded one", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "first-secret");
    // Warms the memo with the first generation's hash.
    expect((await writeEntryReq(prefix, "a", "first-secret")).status).toBe(204);

    await seedBehindTheWorker(prefix, "second-secret");
    expect((await writeEntryReq(prefix, "b", "second-secret")).status).toBe(204);
    expect((await writeEntryReq(prefix, "c", "first-secret")).status).toBe(401);
  });

  // The isolate that serves the initialize is one of many. Every other isolate
  // learns the deploy's hash from an ordinary cache miss, and that memo owes the
  // next generation a re-read exactly as the initialize-seeded one does — or a
  // redeploy is refused for a whole memo lifetime everywhere it did not land.
  it("accepts a redeploy's secret in an isolate whose memo came from a cold fill", async () => {
    const prefix = freshPrefix();
    await seedBehindTheWorker(prefix, "first-secret");
    expect((await writeEntryReq(prefix, "a", "first-secret")).status).toBe(204);

    await seedBehindTheWorker(prefix, "second-secret");
    expect((await writeEntryReq(prefix, "b", "second-secret")).status).toBe(204);
  });

  // A retirement an isolate never saw takes effect there once its memo lapses.
  // The memo is a cache, and this is the bound on how stale it can be.
  it("refuses a write once the deploy has been retired and the memo has lapsed", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");
    expect((await writeEntryReq(prefix, "a", "write-secret")).status).toBe(204);

    const stub = env.ISR_WRITER_DO.get(env.ISR_WRITER_DO.idFromName(prefix));
    await runInDurableObject(stub, (instance) => instance.destroy());

    vi.useFakeTimers();
    vi.setSystemTime(Date.now() + 61_000);
    expect((await writeEntryReq(prefix, "b", "write-secret")).status).toBe(401);
  });

  // A bad token buys at most one registry read per memo generation. The prefix
  // appears verbatim in the R2 paths the edge serves, so it is not secret: were
  // every failed compare to fall through to the DO, anyone who read one could
  // starve that deploy's real writes against a single-threaded object.
  it("re-reads the registry once per memo generation, not once per failed token", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");
    const secretHash = vi.spyOn(IsrDeploy.prototype, "secretHash");

    for (let i = 0; i < 5; i++) {
      expect((await writeEntryReq(prefix, "a", `garbage-${i}`)).status).toBe(401);
    }
    expect(secretHash).toHaveBeenCalledTimes(1);

    // The one re-read is what the rotation case spends, and it is still there
    // for a legitimate token that arrives after the garbage.
    expect((await writeEntryReq(prefix, "a", "write-secret")).status).toBe(204);
    expect(secretHash).toHaveBeenCalledTimes(1);
    secretHash.mockRestore();
  });

  // An unseeded deploy is memoized too, or garbage aimed at a prefix nobody
  // deployed costs a round trip apiece. The first bad token spends both the cold
  // fill and the memo's one re-read; every one after it is refused off the memo.
  it("re-reads the registry twice for an unseeded deploy, however many tokens fail", async () => {
    const prefix = freshPrefix();
    const secretHash = vi.spyOn(IsrDeploy.prototype, "secretHash");

    for (let i = 0; i < 5; i++) {
      expect((await writeEntryReq(prefix, "a", `garbage-${i}`)).status).toBe(401);
    }
    expect(secretHash).toHaveBeenCalledTimes(2);
    secretHash.mockRestore();
  });
});

// The single writer of a build's tag-clock replica, reached with the same
// per-deploy write secret every other runtime op carries. A 2xx here is a
// promise the caller leans on: the raiser reads its own write immediately
// afterwards, so an answer that outran the R2 write would read as an
// invalidation that never happened.
describe("tag raises", () => {
  function raiseReq(prefix: string, secret: string, body: unknown) {
    return SELF.fetch(
      bearerReq(`/${prefix}/tags`, secret, { method: "POST", body: JSON.stringify(body) }),
    );
  }

  async function seedGenesis(prefix: string, deployedAt: number) {
    const genesis: TagSnapshot = { version: 1, deployedAt, generatedAt: deployedAt, records: {} };
    await env.OCEL_CACHE_STORE.put(tagSnapshotKey(prefix), JSON.stringify(genesis));
  }

  async function snapshotOf(prefix: string): Promise<TagSnapshot | null> {
    const object = await env.OCEL_CACHE_STORE.get(tagSnapshotKey(prefix));
    return object === null ? null : ((await object.json()) as TagSnapshot);
  }

  function snapshotStub(prefix: string) {
    return env.ISR_SNAPSHOT_DO.get(env.ISR_SNAPSHOT_DO.idFromName(prefix));
  }

  it("leaves the R2 document holding the raised records before it answers", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");
    await seedGenesis(prefix, 1_000);

    const res = await raiseReq(prefix, "write-secret", {
      records: { products: { expired: 5_000 }, blog: { stale: 6_000 } },
    });
    expect(res.status).toBe(204);
    expect((await snapshotOf(prefix))?.records).toEqual({
      products: { expired: 5_000 },
      blog: { stale: 6_000 },
    });
  });

  it("keeps the deploy anchor the genesis seed wrote", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");
    await seedGenesis(prefix, 1_000);

    await raiseReq(prefix, "write-secret", { records: { a: { expired: 5_000 } } });
    expect((await snapshotOf(prefix))?.deployedAt).toBe(1_000);
  });

  it("refuses a raise signed with another deploy's secret, and an unsigned one", async () => {
    const mine = freshPrefix();
    const theirs = freshPrefix();
    await initialize(mine, "my-secret");
    await initialize(theirs, "their-secret");
    await seedGenesis(theirs, 1_000);

    expect((await raiseReq(theirs, "my-secret", { records: { a: { expired: 5_000 } } })).status).toBe(401);
    expect((await raiseReq(theirs, BOOTSTRAP, { records: { a: { expired: 5_000 } } })).status).toBe(401);
    const unsigned = await SELF.fetch(
      req(`/${theirs}/tags`, { method: "POST", body: JSON.stringify({ records: {} }) }),
    );
    expect(unsigned.status).toBe(401);
    expect((await snapshotOf(theirs))?.records).toEqual({});
  });

  it("refuses a body that is not a set of tag records", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");

    const bodies = [
      {},
      { records: null },
      { records: [] },
      { records: { a: 7 } },
      { records: { a: {} } },
      { records: { a: { expired: "5000" } } },
      { records: { a: { expired: -1 } } },
      { records: { "": { expired: 5_000 } } },
    ];
    for (const body of bodies) {
      expect((await raiseReq(prefix, "write-secret", body)).status, JSON.stringify(body)).toBe(400);
    }
  });

  // Nothing durable happened, and the caller still holds the records: the merge
  // is idempotent, so raising them again is the whole of the repair. Reporting
  // it is what the three-attempt silent give-up never did.
  it("reports an exhausted publish as a rate limit rather than a success", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");
    const raise = vi.spyOn(IsrSnapshot.prototype, "raise").mockResolvedValue("exhausted");

    expect((await raiseReq(prefix, "write-secret", { records: { a: { expired: 5_000 } } })).status).toBe(429);
    raise.mockRestore();
  });

  it("404s a raise against a path that is not a deploy prefix", async () => {
    const res = await SELF.fetch(
      bearerReq("/prod/acme/tags", "write-secret", { method: "POST", body: "{}" }),
    );
    expect(res.status).toBe(404);
  });

  // The document carries no expiry, so an untouched object otherwise means both
  // "nothing has changed" and "nobody has published in a week".
  it("advances generatedAt from the heartbeat alarm with no tag activity", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");
    await seedGenesis(prefix, 1_000);
    await raiseReq(prefix, "write-secret", { records: { a: { expired: 5_000 } } });

    const published = (await snapshotOf(prefix))!;
    expect(await runDurableObjectAlarm(snapshotStub(prefix))).toBe(true);

    const beaten = (await snapshotOf(prefix))!;
    expect(beaten.generatedAt).toBeGreaterThanOrEqual(published.generatedAt);
    expect(beaten.records).toEqual(published.records);
    expect(beaten.deployedAt).toBe(1_000);
    // Liveness that stops after one beat is no liveness at all.
    expect(await runDurableObjectAlarm(snapshotStub(prefix))).toBe(true);
  });

  // A build whose snapshot is gone has been retired or pruned. A document
  // conjured for it would carry no deploy anchor, so it could never prune again
  // — and it would be a replica of a build that no longer exists.
  it("stops beating for a build that has no snapshot to republish", async () => {
    const prefix = freshPrefix();
    const stub = snapshotStub(prefix);
    // What an object evicted between beats wakes up to: its claim on a build,
    // an alarm due, and nothing in R2 under that build's key.
    await runInDurableObject(stub, async (_instance, ctx) => {
      await claimBuild(ctx.storage, prefix);
      await ctx.storage.setAlarm(Date.now() + 60_000);
    });

    expect(await runDurableObjectAlarm(stub)).toBe(true);
    expect(await snapshotOf(prefix)).toBeNull();
    expect(await runDurableObjectAlarm(stub)).toBe(false);
  });

  it("stops beating for a deploy that has been retired", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");
    await seedGenesis(prefix, 1_000);
    await raiseReq(prefix, "write-secret", { records: { a: { expired: 5_000 } } });

    const res = await SELF.fetch(bearerReq(`/${prefix}/destroy`, BOOTSTRAP, { method: "POST" }));
    expect(res.status).toBe(204);
    expect(await runDurableObjectAlarm(snapshotStub(prefix))).toBe(false);
  });
});
