import { env } from "cloudflare:test";
import { describe, expect, it } from "vitest";

import type { DeploymentRecord, Promotion } from "../src/store";
import type { Env } from "../src/env";

declare module "cloudflare:test" {
  interface ProvidedEnv extends Env {}
}

// Every test gets a fresh DO instance (isolatedStorage snapshots storage per
// test), so a stable name is fine to reuse across tests.
function storeStub() {
  const id = env.DEPLOYMENTS_DO.idFromName("root");
  return env.DEPLOYMENTS_DO.get(id);
}

function makeRecord(over: Partial<DeploymentRecord> = {}): DeploymentRecord {
  return {
    app: "web",
    buildId: "build-1",
    routingManifest: { pathnames: [] },
    functionUrls: { "/": "https://fn.example.com" },
    tagNamespace: "ns-1",
    assetPrefix: "build-1",
    createdAt: 1_000,
    ...over,
  };
}

function makePromotion(over: Partial<Promotion> = {}): Promotion {
  return {
    promotionId: "promo-1",
    ts: 1_000,
    builds: { web: "build-1" },
    ...over,
  };
}

describe("putStaged", () => {
  it("stores a record without changing the active pointer", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord());

    expect(await store.record("web", "build-1")).toEqual(makeRecord());
    expect(await store.activeBuildId("web")).toBeUndefined();
    expect(await store.history()).toEqual([]);
  });
});

describe("promote", () => {
  it("flips the active pointer atomically and appends to history", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord());

    await store.promote(makePromotion());

    expect(await store.activeBuildId("web")).toBe("build-1");
    expect(await store.history()).toEqual([
      { promotionId: "promo-1", ts: 1_000, builds: { web: "build-1" }, active: true },
    ]);
  });

  it("moves the active pointer across successive promotions", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord({ buildId: "build-1" }));
    await store.putStaged(makeRecord({ buildId: "build-2" }));

    await store.promote(makePromotion({ promotionId: "promo-1", ts: 1_000, builds: { web: "build-1" } }));
    await store.promote(makePromotion({ promotionId: "promo-2", ts: 2_000, builds: { web: "build-2" } }));

    expect(await store.activeBuildId("web")).toBe("build-2");
  });
});

describe("activeBuildId / record", () => {
  it("derives the active build id from the active promotion", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord({ app: "web", buildId: "build-1" }));
    await store.putStaged(makeRecord({ app: "docs", buildId: "build-9" }));
    await store.promote(makePromotion({ builds: { web: "build-1", docs: "build-9" } }));

    expect(await store.activeBuildId("web")).toBe("build-1");
    expect(await store.activeBuildId("docs")).toBe("build-9");
  });

  it("returns undefined for an app with no active promotion", async () => {
    const store = storeStub();
    expect(await store.activeBuildId("nonexistent")).toBeUndefined();
  });

  it("returns the stored record for a given app and build id", async () => {
    const store = storeStub();
    const record = makeRecord({ tagNamespace: "custom-ns" });
    await store.putStaged(record);

    expect(await store.record("web", "build-1")).toEqual(record);
    expect(await store.record("web", "no-such-build")).toBeUndefined();
  });
});

describe("history", () => {
  it("returns promotions newest-first with the active one marked", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord({ buildId: "build-1" }));
    await store.putStaged(makeRecord({ buildId: "build-2" }));
    await store.promote(makePromotion({ promotionId: "promo-1", ts: 1_000, builds: { web: "build-1" } }));
    await store.promote(makePromotion({ promotionId: "promo-2", ts: 2_000, builds: { web: "build-2" } }));

    expect(await store.history()).toEqual([
      { promotionId: "promo-2", ts: 2_000, builds: { web: "build-2" }, active: true },
      { promotionId: "promo-1", ts: 1_000, builds: { web: "build-1" }, active: false },
    ]);
  });
});

describe("prune", () => {
  async function seedPromotions(store: ReturnType<typeof storeStub>, n: number) {
    for (let i = 1; i <= n; i++) {
      await store.putStaged(makeRecord({ buildId: `build-${i}` }));
      await store.promote(
        makePromotion({ promotionId: `promo-${i}`, ts: i * 1_000, builds: { web: `build-${i}` } }),
      );
    }
  }

  it("removes promotions beyond keepN, newest first", async () => {
    const store = storeStub();
    await seedPromotions(store, 5);

    const result = await store.prune(3);

    expect(result.keptPromotionIds).toEqual(["promo-5", "promo-4", "promo-3"]);
    expect(result.removedPromotionIds).toEqual(["promo-2", "promo-1"]);
    expect((await store.history()).map((h) => h.promotionId)).toEqual([
      "promo-5",
      "promo-4",
      "promo-3",
    ]);
  });

  it("deletes the records of removed promotions", async () => {
    const store = storeStub();
    await seedPromotions(store, 4);

    const result = await store.prune(2);

    expect(result.removedRecordKeys).toEqual(["record:web/build-2", "record:web/build-1"]);
    expect(await store.record("web", "build-1")).toBeUndefined();
    expect(await store.record("web", "build-2")).toBeUndefined();
    expect(await store.record("web", "build-3")).toEqual(makeRecord({ buildId: "build-3" }));
  });

  it("pins the active promotion even when it falls outside the keep window", async () => {
    const store = storeStub();
    await seedPromotions(store, 5);
    // Roll back to an old promotion before pruning, so the active one is
    // outside the naive "most recent N" window.
    await store.promote(makePromotion({ promotionId: "promo-1", ts: 6_000, builds: { web: "build-1" } }));

    const result = await store.prune(2);

    expect(result.keptPromotionIds).toContain("promo-1");
    expect(await store.record("web", "build-1")).toBeDefined();
  });

  it("never removes more than requested when history is already within the window", async () => {
    const store = storeStub();
    await seedPromotions(store, 2);

    const result = await store.prune(10);

    expect(result.removedPromotionIds).toEqual([]);
    expect(result.keptPromotionIds).toEqual(["promo-2", "promo-1"]);
  });
});

describe("version stamp", () => {
  it("is readable and updatable", async () => {
    const store = storeStub();
    expect(await store.versionStamp()).toBeUndefined();

    await store.setVersionStamp("v1");
    expect(await store.versionStamp()).toBe("v1");

    await store.setVersionStamp("v2");
    expect(await store.versionStamp()).toBe("v2");
  });
});
