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

  it("accepts a trailing-slash route key, which the direct write path accepted", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");

    expect((await writeEntryReq(prefix, "blog/", "write-secret", `{"value":9}`)).status).toBe(204);

    const stored = await env.OCEL_CACHE_STORE.get(`${prefix}/cache/blog/.cache.json`);
    expect(await stored?.text()).toBe(`{"value":9}`);
  });
});

describe("entry reads", () => {
  function readEntryReq(prefix: string, key: string, secret: string) {
    return SELF.fetch(bearerReq(`/${prefix}/entry?key=${encodeURIComponent(key)}`, secret));
  }

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

  it.each(["-H1t_CFb4Ec1S1wr0e2T4", "_H1t-CFb4Ec1S1wr0e2T4"])(
    "accepts the build id %s, which a nanoid leads with once in thirty builds",
    async (buildId) => {
      const prefix = `preview-p/p/app/${buildId}`;
      expect((await initialize(prefix, "write-secret")).status).toBe(204);
      expect((await writeEntryReq(prefix, "blog/post", "write-secret")).status).toBe(204);
    },
  );
});

async function seedBehindTheWorker(prefix: string, secret: string) {
  const stub = env.ISR_WRITER_DO.get(env.ISR_WRITER_DO.idFromName(prefix));
  const hash = await sha256Hex(secret);
  await runInDurableObject(stub, (_instance, ctx) =>
    registry.initialize(ctx.storage, hash),
  );
}

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
    expect((await writeEntryReq(prefix, "a", "first-secret")).status).toBe(204);

    await seedBehindTheWorker(prefix, "second-secret");
    expect((await writeEntryReq(prefix, "b", "second-secret")).status).toBe(204);
    expect((await writeEntryReq(prefix, "c", "first-secret")).status).toBe(401);
  });

  it("accepts a redeploy's secret in an isolate whose memo came from a cold fill", async () => {
    const prefix = freshPrefix();
    await seedBehindTheWorker(prefix, "first-secret");
    expect((await writeEntryReq(prefix, "a", "first-secret")).status).toBe(204);

    await seedBehindTheWorker(prefix, "second-secret");
    expect((await writeEntryReq(prefix, "b", "second-secret")).status).toBe(204);
  });

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

  it("re-reads the registry once per memo generation, not once per failed token", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");
    const secretHash = vi.spyOn(IsrDeploy.prototype, "secretHash");

    for (let i = 0; i < 5; i++) {
      expect((await writeEntryReq(prefix, "a", `garbage-${i}`)).status).toBe(401);
    }
    expect(secretHash).toHaveBeenCalledTimes(1);

    expect((await writeEntryReq(prefix, "a", "write-secret")).status).toBe(204);
    expect(secretHash).toHaveBeenCalledTimes(1);
    secretHash.mockRestore();
  });

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
    expect(await runDurableObjectAlarm(snapshotStub(prefix))).toBe(true);
  });

  it("beats for a build that has been deployed and never invalidated", async () => {
    const prefix = freshPrefix();
    await seedGenesis(prefix, 1_000);
    await initialize(prefix, "write-secret");

    expect(await runDurableObjectAlarm(snapshotStub(prefix))).toBe(true);

    const beaten = (await snapshotOf(prefix))!;
    expect(beaten.generatedAt).toBeGreaterThan(1_000);
    expect(beaten.deployedAt).toBe(1_000);
    expect(beaten.records).toEqual({});
    expect(await runDurableObjectAlarm(snapshotStub(prefix))).toBe(true);
  });

  it("creates no snapshot for a deploy whose genesis never landed", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "write-secret");

    expect(await runDurableObjectAlarm(snapshotStub(prefix))).toBe(true);
    expect(await snapshotOf(prefix)).toBeNull();
  });

  it("stops beating for a build that has no snapshot to republish", async () => {
    const prefix = freshPrefix();
    const stub = snapshotStub(prefix);
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
