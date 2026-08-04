import { SELF, env, runInDurableObject } from "cloudflare:test";
import { afterEach, describe, expect, it, vi } from "vitest";

import { sha256Hex } from "../src/auth";
import { IsrDeploy } from "../src/isr-deploy";
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

describe("secret rotation", () => {
  // A redeploy of the same build reseeds the same isrPrefix with a freshly
  // derived secret. Reseeding behind the worker's back — straight into the DO's
  // storage — is what an isolate that never saw the initialize sees, so this is
  // the case where the memo is genuinely a generation behind.
  async function reseedBehindTheWorker(prefix: string, secret: string) {
    const stub = env.ISR_WRITER_DO.get(env.ISR_WRITER_DO.idFromName(prefix));
    const hash = await sha256Hex(secret);
    await runInDurableObject(stub, (_instance, ctx) =>
      registry.initialize(ctx.storage, hash),
    );
  }

  it("accepts the reseeded secret and refuses the superseded one", async () => {
    const prefix = freshPrefix();
    await initialize(prefix, "first-secret");
    // Warms the memo with the first generation's hash.
    expect((await writeEntryReq(prefix, "a", "first-secret")).status).toBe(204);

    await reseedBehindTheWorker(prefix, "second-secret");
    expect((await writeEntryReq(prefix, "b", "second-secret")).status).toBe(204);
    expect((await writeEntryReq(prefix, "c", "first-secret")).status).toBe(401);
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
  // deployed costs a round trip apiece.
  it("re-reads the registry once for an unseeded deploy, however many tokens fail", async () => {
    const prefix = freshPrefix();
    const secretHash = vi.spyOn(IsrDeploy.prototype, "secretHash");

    for (let i = 0; i < 5; i++) {
      expect((await writeEntryReq(prefix, "a", `garbage-${i}`)).status).toBe(401);
    }
    expect(secretHash).toHaveBeenCalledTimes(1);
    secretHash.mockRestore();
  });
});
