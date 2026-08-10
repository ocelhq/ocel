import { env, runInDurableObject } from "cloudflare:test";
import { describe, expect, it } from "vitest";

import * as registry from "../src/registry";

async function withStorage<T>(name: string, fn: (store: registry.SqlStore) => T): Promise<T> {
  const id = env.ISR_WRITER_DO.idFromName(name);
  return runInDurableObject(env.ISR_WRITER_DO.get(id), (_instance, ctx) => fn(ctx.storage));
}

describe("registry", () => {
  it("reads back nothing before a deploy is initialized", async () => {
    expect(await withStorage("prod/p/web/EMPTY", registry.secretHash)).toBeUndefined();
  });

  it("stores and reads back one deploy's secret hash", async () => {
    const hash = "a".repeat(64);
    await withStorage("prod/p/web/B1", (store) => registry.initialize(store, hash));
    expect(await withStorage("prod/p/web/B1", registry.secretHash)).toBe(hash);
  });

  it("replaces the hash on re-initialize rather than accumulating rows", async () => {
    await withStorage("prod/p/web/B2", (store) => registry.initialize(store, "b".repeat(64)));
    await withStorage("prod/p/web/B2", (store) => registry.initialize(store, "c".repeat(64)));
    expect(await withStorage("prod/p/web/B2", registry.secretHash)).toBe("c".repeat(64));
  });

  it("keeps deploys isolated from one another", async () => {
    await withStorage("prod/p/web/B3", (store) => registry.initialize(store, "d".repeat(64)));
    expect(await withStorage("prod/p/web/B4", registry.secretHash)).toBeUndefined();
  });
});
