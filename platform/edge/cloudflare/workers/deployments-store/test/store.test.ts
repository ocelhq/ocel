import { env } from "cloudflare:test";
import { describe, expect, it } from "vitest";

import type { DeploymentRecord, Promotion } from "../src/store";
import type { Env } from "../src/env";

declare module "cloudflare:test" {
  interface ProvidedEnv extends Env {}
}

function storeStub() {
  const id = env.DEPLOYMENTS_DO.idFromName("acme-web");
  return env.DEPLOYMENTS_DO.get(id);
}

function makeRecord(over: Partial<DeploymentRecord> = {}): DeploymentRecord {
  return {
    app: "web",
    framework: "next",
    deploymentId: "deploy-1",
    buildId: "build-1",
    routingManifest: { pathnames: [] },
    functionUrls: { "/": "https://fn.example.com" },
    assetPrefix: "deploy-1",
    isrPrefix: "prod/p1/web/build-1",
    createdAt: 1_000,
    ...over,
  };
}

function makePromotion(over: Partial<Promotion> = {}): Promotion {
  return {
    promotionId: "promo-1",
    ts: 1_000,
    builds: { web: "deploy-1" },
    ...over,
  };
}

describe("putStaged", () => {
  it("stores a record without changing the active pointer", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord());

    expect(await store.record("web", "deploy-1")).toEqual(makeRecord());
    expect(await store.pointerDeploymentId("web")).toBeUndefined();
    expect(await store.history()).toEqual([]);
  });
});

describe("promote", () => {
  it("flips the active pointer atomically and appends to history", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord());

    await store.promote(makePromotion());

    expect(await store.pointerDeploymentId("web")).toBe("deploy-1");
    expect(await store.history()).toEqual([
      { promotionId: "promo-1", ts: 1_000, builds: { web: "deploy-1" }, active: true },
    ]);
  });

  it("moves the active pointer across successive promotions", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord({ deploymentId: "deploy-1" }));
    await store.putStaged(makeRecord({ deploymentId: "deploy-2" }));

    await store.promote(makePromotion({ promotionId: "promo-1", ts: 1_000, builds: { web: "deploy-1" } }));
    await store.promote(makePromotion({ promotionId: "promo-2", ts: 2_000, builds: { web: "deploy-2" } }));

    expect(await store.pointerDeploymentId("web")).toBe("deploy-2");
  });
});

describe("named pointers", () => {
  it("promotes to a named pointer without moving the default one", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord({ deploymentId: "prod-deploy" }));
    await store.putStaged(makeRecord({ deploymentId: "preview-deploy" }));

    await store.promote(
      makePromotion({ promotionId: "prod", builds: { web: "prod-deploy" } }),
    );
    await store.promote(
      makePromotion({ promotionId: "prev", builds: { web: "preview-deploy" } }),
      "flaky-web-2626",
    );

    expect(await store.pointerDeploymentId("web")).toBe("prod-deploy");
    expect(await store.pointerDeploymentId("web", "flaky-web-2626")).toBe(
      "preview-deploy",
    );
  });

  it("resolves a named pointer's record through pointerRecord", async () => {
    const store = storeStub();
    const record = makeRecord({ deploymentId: "preview-deploy" });
    await store.putStaged(record);
    await store.promote(
      makePromotion({ promotionId: "prev", builds: { web: "preview-deploy" } }),
      "flaky-web-2626",
    );

    expect(await store.pointerRecord("web", "flaky-web-2626")).toEqual({
      kind: "record",
      deploymentId: "preview-deploy",
      record,
    });
    expect(await store.pointerRecord("web", "no-such-preview")).toEqual({
      kind: "no-pointer",
    });
  });

  it("re-promoting a named pointer moves only that pointer", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord({ deploymentId: "preview-1" }));
    await store.putStaged(makeRecord({ deploymentId: "preview-2" }));

    await store.promote(
      makePromotion({ promotionId: "p1", builds: { web: "preview-1" } }),
      "preview",
    );
    await store.promote(
      makePromotion({ promotionId: "p2", builds: { web: "preview-2" } }),
      "preview",
    );

    expect(await store.pointerDeploymentId("web", "preview")).toBe("preview-2");
    expect(await store.pointerDeploymentId("web")).toBeUndefined();
  });
});

describe("pointerDeploymentId / record", () => {
  it("derives the active deployment id from the active promotion", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord({ app: "web", deploymentId: "deploy-1" }));
    await store.putStaged(makeRecord({ app: "docs", deploymentId: "deploy-9" }));
    await store.promote(makePromotion({ builds: { web: "deploy-1", docs: "deploy-9" } }));

    expect(await store.pointerDeploymentId("web")).toBe("deploy-1");
    expect(await store.pointerDeploymentId("docs")).toBe("deploy-9");
  });

  it("returns undefined for an app with no active promotion", async () => {
    const store = storeStub();
    expect(await store.pointerDeploymentId("nonexistent")).toBeUndefined();
  });

  it("returns the stored record for a given app and deployment id", async () => {
    const store = storeStub();
    const record = makeRecord({ isrPrefix: "custom-isr" });
    await store.putStaged(record);

    expect(await store.record("web", "deploy-1")).toEqual(record);
    expect(await store.record("web", "no-such-deployment")).toBeUndefined();
  });

  it("keeps two deployments of one build id apart", async () => {
    const store = storeStub();
    const first = makeRecord({ deploymentId: "deploy-1", buildId: "build-TfctsWXpff2fKS" });
    const second = makeRecord({ deploymentId: "deploy-2", buildId: "build-TfctsWXpff2fKS" });
    await store.putStaged(first);
    await store.putStaged(second);
    await store.promote(
      makePromotion({ promotionId: "promo-1", builds: { web: "deploy-1" } }),
      "pr-1",
    );
    await store.promote(
      makePromotion({ promotionId: "promo-2", builds: { web: "deploy-2" } }),
      "pr-2",
    );

    expect(await store.record("web", "deploy-1")).toEqual(first);
    expect(await store.record("web", "deploy-2")).toEqual(second);
    expect(await store.pointerRecord("web", "pr-1")).toEqual({
      kind: "record",
      deploymentId: "deploy-1",
      record: first,
    });

    await store.removePointer("pr-1");

    expect(await store.record("web", "deploy-1")).toBeUndefined();
    expect(await store.pointerRecord("web", "pr-2")).toEqual({
      kind: "record",
      deploymentId: "deploy-2",
      record: second,
    });
  });
});

describe("pointerRecord", () => {
  it("returns no-pointer when the app has no active promotion", async () => {
    const store = storeStub();
    expect(await store.pointerRecord("web")).toEqual({ kind: "no-pointer" });
  });

  it("returns the active build id and record when no build is known", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord());
    await store.promote(makePromotion());

    expect(await store.pointerRecord("web")).toEqual({
      kind: "record",
      deploymentId: "deploy-1",
      record: makeRecord(),
    });
  });

  it("omits the record when the known build is still active", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord());
    await store.promote(makePromotion());

    expect(await store.pointerRecord("web", undefined, "deploy-1")).toEqual({
      kind: "unchanged",
      deploymentId: "deploy-1",
    });
  });

  it("returns the new record when the known build is stale", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord({ deploymentId: "deploy-1" }));
    await store.putStaged(makeRecord({ deploymentId: "deploy-2" }));
    await store.promote(makePromotion({ promotionId: "promo-1", builds: { web: "deploy-1" } }));
    await store.promote(makePromotion({ promotionId: "promo-2", ts: 2_000, builds: { web: "deploy-2" } }));

    expect(await store.pointerRecord("web", undefined, "deploy-1")).toEqual({
      kind: "record",
      deploymentId: "deploy-2",
      record: makeRecord({ deploymentId: "deploy-2" }),
    });
  });

  it("returns dangling when the active pointer names a build with no record", async () => {
    const store = storeStub();
    await store.promote(makePromotion({ builds: { web: "ghost-deploy" } }));

    expect(await store.pointerRecord("web")).toEqual({
      kind: "dangling",
      deploymentId: "ghost-deploy",
    });
  });

  it("resolves the promotion's sole app when no app is given", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord());
    await store.promote(makePromotion());

    expect(await store.pointerRecord(undefined)).toEqual({
      kind: "record",
      deploymentId: "deploy-1",
      record: makeRecord(),
    });
    expect(await store.pointerRecord(undefined, undefined, "deploy-1")).toEqual({
      kind: "unchanged",
      deploymentId: "deploy-1",
    });
  });

  it("returns ambiguous-app when the promotion carries more than one app", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord());
    await store.putStaged(makeRecord({ app: "admin", deploymentId: "deploy-9" }));
    await store.promote(
      makePromotion({ builds: { web: "deploy-1", admin: "deploy-9" } }),
    );

    expect(await store.pointerRecord(undefined)).toEqual({
      kind: "ambiguous-app",
    });
    expect(await store.pointerRecord("admin")).toEqual({
      kind: "record",
      deploymentId: "deploy-9",
      record: makeRecord({ app: "admin", deploymentId: "deploy-9" }),
    });
  });

  it("returns no-pointer without an app when the pointer has no promotion", async () => {
    const store = storeStub();
    expect(await store.pointerRecord(undefined, "flaky-web-2626")).toEqual({
      kind: "no-pointer",
    });
  });

  it("resolves the sole app of a named pointer", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord({ deploymentId: "preview-1" }));
    await store.promote(
      makePromotion({ promotionId: "promo-p", builds: { web: "preview-1" } }),
      "flaky-web-2626",
    );

    expect(await store.pointerRecord(undefined, "flaky-web-2626")).toEqual({
      kind: "record",
      deploymentId: "preview-1",
      record: makeRecord({ deploymentId: "preview-1" }),
    });
  });
});

describe("history", () => {
  it("returns promotions newest-first with the active one marked", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord({ deploymentId: "deploy-1" }));
    await store.putStaged(makeRecord({ deploymentId: "deploy-2" }));
    await store.promote(makePromotion({ promotionId: "promo-1", ts: 1_000, builds: { web: "deploy-1" } }));
    await store.promote(makePromotion({ promotionId: "promo-2", ts: 2_000, builds: { web: "deploy-2" } }));

    expect(await store.history()).toEqual([
      { promotionId: "promo-2", ts: 2_000, builds: { web: "deploy-2" }, active: true },
      { promotionId: "promo-1", ts: 1_000, builds: { web: "deploy-1" }, active: false },
    ]);
  });

  it("carries a promotion's tag through history", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord());
    await store.promote(makePromotion({ tag: "v1.2.3" }));

    expect(await store.history()).toEqual([
      { promotionId: "promo-1", ts: 1_000, builds: { web: "deploy-1" }, tag: "v1.2.3", active: true },
    ]);
  });
});

describe("tags", () => {
  it("rejects a tag already held by a different promotion", async () => {
    const store = storeStub();
    await store.promote(makePromotion({ promotionId: "promo-1", tag: "v1.2.3" }));

    const { conflict } = await store.promote(
      makePromotion({ promotionId: "promo-2", tag: "v1.2.3" }),
    );

    expect(conflict).toMatch(/already used by promotion promo-1/);
    expect((await store.history()).map((h) => h.promotionId)).toEqual(["promo-1"]);
    expect(await store.pointerDeploymentId("web")).toBe("deploy-1");
  });

  it("lets a rollback re-promote its own tagged id without a self-conflict", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord({ deploymentId: "deploy-1" }));
    await store.putStaged(makeRecord({ deploymentId: "deploy-2" }));
    await store.promote(
      makePromotion({ promotionId: "promo-1", ts: 1_000, tag: "v1", builds: { web: "deploy-1" } }),
    );
    await store.promote(
      makePromotion({ promotionId: "promo-2", ts: 2_000, builds: { web: "deploy-2" } }),
    );

    const { conflict } = await store.promote(
      makePromotion({ promotionId: "promo-1", ts: 3_000, tag: "v1", builds: { web: "deploy-1" } }),
    );

    expect(conflict).toBeUndefined();
    expect(await store.pointerDeploymentId("web")).toBe("deploy-1");
    expect((await store.history()).map((h) => h.promotionId)).toEqual(["promo-1", "promo-2"]);
  });

  it("frees a tag once its promotion is pruned", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord({ deploymentId: "deploy-1" }));
    await store.putStaged(makeRecord({ deploymentId: "deploy-2" }));
    await store.putStaged(makeRecord({ deploymentId: "deploy-3" }));
    await store.promote(
      makePromotion({ promotionId: "promo-1", ts: 1_000, tag: "v1", builds: { web: "deploy-1" } }),
    );
    await store.promote(
      makePromotion({ promotionId: "promo-2", ts: 2_000, builds: { web: "deploy-2" } }),
    );
    await store.prune(1); // keeps promo-2 (active); removes promo-1 and frees "v1"

    const { conflict } = await store.promote(
      makePromotion({ promotionId: "promo-3", ts: 3_000, tag: "v1", builds: { web: "deploy-3" } }),
    );

    expect(conflict).toBeUndefined();
    expect((await store.history()).find((h) => h.tag === "v1")?.promotionId).toBe("promo-3");
  });
});

describe("prune", () => {
  async function seedPromotions(store: ReturnType<typeof storeStub>, n: number) {
    for (let i = 1; i <= n; i++) {
      await store.putStaged(makeRecord({ deploymentId: `deploy-${i}` }));
      await store.promote(
        makePromotion({ promotionId: `promo-${i}`, ts: i * 1_000, builds: { web: `deploy-${i}` } }),
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

    expect(result.removedRecordKeys).toEqual(["record:web/deploy-2", "record:web/deploy-1"]);
    expect(await store.record("web", "deploy-1")).toBeUndefined();
    expect(await store.record("web", "deploy-2")).toBeUndefined();
    expect(await store.record("web", "deploy-3")).toEqual(makeRecord({ deploymentId: "deploy-3" }));
  });

  it("reports the record keys the store still holds", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord({ deploymentId: "deploy-1" }));
    await store.promote(makePromotion({ promotionId: "promo-1", builds: { web: "deploy-1" } }));
    await store.putStaged(makeRecord({ deploymentId: "deploy-1~fp2" }));
    await store.promote(
      makePromotion({ promotionId: "promo-2", ts: 2_000, builds: { web: "deploy-1~fp2" } }),
    );

    const result = await store.prune(1);

    expect(result.removedRecordKeys).toEqual(["record:web/deploy-1"]);
    expect(result.survivingRecordKeys).toEqual(["record:web/deploy-1~fp2"]);
  });

  it("reports the pruned pointer's own surviving record keys separately", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord({ deploymentId: "deploy-1~fpP" }));
    await store.promote(makePromotion({ promotionId: "promo-1", builds: { web: "deploy-1~fpP" } }));
    await store.putStaged(makeRecord({ deploymentId: "deploy-2" }));
    await store.promote(
      makePromotion({ promotionId: "promo-2", ts: 2_000, builds: { web: "deploy-2" } }),
    );
    await store.putStaged(makeRecord({ deploymentId: "deploy-1~fpV" }));
    await store.promote(
      makePromotion({ promotionId: "prev-1", ts: 3_000, builds: { web: "deploy-1~fpV" } }),
      "pr-7",
    );

    const result = await store.prune(1);

    expect(result.removedRecordKeys).toEqual(["record:web/deploy-1~fpP"]);
    expect(result.survivingRecordKeys).toEqual([
      "record:web/deploy-1~fpV",
      "record:web/deploy-2",
    ]);
    expect(result.survivingPointerRecordKeys).toEqual(["record:web/deploy-2"]);
  });

  it("pins the active promotion even when it falls outside the keep window", async () => {
    const store = storeStub();
    await seedPromotions(store, 5);
    await store.promote(makePromotion({ promotionId: "promo-1", ts: 6_000, builds: { web: "deploy-1" } }));

    const result = await store.prune(2);

    expect(result.keptPromotionIds).toContain("promo-1");
    expect(await store.record("web", "deploy-1")).toBeDefined();
  });

  it("never removes more than requested when history is already within the window", async () => {
    const store = storeStub();
    await seedPromotions(store, 2);

    const result = await store.prune(10);

    expect(result.removedPromotionIds).toEqual([]);
    expect(result.keptPromotionIds).toEqual(["promo-2", "promo-1"]);
  });

  it("is scoped to a pointer: pruning one pointer leaves another untouched", async () => {
    const store = storeStub();
    for (let i = 1; i <= 3; i++) {
      await store.putStaged(makeRecord({ deploymentId: `prod-${i}` }));
      await store.promote(
        makePromotion({ promotionId: `prod-${i}`, ts: i * 1_000, builds: { web: `prod-${i}` } }),
      );
      await store.putStaged(makeRecord({ deploymentId: `prev-${i}` }));
      await store.promote(
        makePromotion({ promotionId: `prev-${i}`, ts: i * 1_000 + 500, builds: { web: `prev-${i}` } }),
        "staging",
      );
    }

    const result = await store.prune(1, "staging");

    expect(result.removedPromotionIds).toEqual(["prev-2", "prev-1"]);
    expect((await store.history("staging")).map((h) => h.promotionId)).toEqual(["prev-3"]);
    expect((await store.history()).map((h) => h.promotionId)).toEqual([
      "prod-3",
      "prod-2",
      "prod-1",
    ]);
    expect(await store.record("web", "prod-1")).toBeDefined();
    expect(await store.record("web", "prev-1")).toBeUndefined();
  });
});

describe("removePointer", () => {
  it("removes a whole pointer (active included) and reports what to reclaim", async () => {
    const store = storeStub();
    for (let i = 1; i <= 2; i++) {
      await store.putStaged(makeRecord({ deploymentId: `prod-${i}` }));
      await store.promote(
        makePromotion({ promotionId: `prod-${i}`, ts: i * 1_000, builds: { web: `prod-${i}` } }),
      );
      await store.putStaged(makeRecord({ deploymentId: `prev-${i}` }));
      await store.promote(
        makePromotion({ promotionId: `prev-${i}`, ts: i * 1_000 + 500, builds: { web: `prev-${i}` } }),
        "staging",
      );
    }

    const result = await store.removePointer("staging");

    expect(result.keptPromotionIds).toEqual([]);
    expect(result.removedPromotionIds).toEqual(["prev-2", "prev-1"]);
    expect(result.removedRecordKeys.sort()).toEqual(
      ["record:web/prev-1", "record:web/prev-2"].sort(),
    );
    expect(await store.history("staging")).toEqual([]);
    expect(await store.pointerDeploymentId("web", "staging")).toBeUndefined();
    expect(await store.record("web", "prev-1")).toBeUndefined();
    expect((await store.history()).map((h) => h.promotionId)).toEqual(["prod-2", "prod-1"]);
    expect(await store.record("web", "prod-1")).toBeDefined();
  });

  it("leaves the removed pointer with no surviving record keys of its own", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord({ deploymentId: "deploy-1~fpP" }));
    await store.promote(makePromotion({ promotionId: "prod-1", builds: { web: "deploy-1~fpP" } }));
    await store.putStaged(makeRecord({ deploymentId: "deploy-1~fpV" }));
    await store.promote(
      makePromotion({ promotionId: "prev-1", ts: 2_000, builds: { web: "deploy-1~fpV" } }),
      "staging",
    );

    const result = await store.removePointer("staging");

    expect(result.survivingRecordKeys).toEqual(["record:web/deploy-1~fpP"]);
    expect(result.survivingPointerRecordKeys).toEqual([]);
  });

  it("removing an unknown pointer is a clean no-op", async () => {
    const store = storeStub();
    const result = await store.removePointer("never-existed");
    expect(result.removedPromotionIds).toEqual([]);
    expect(result.removedRecordKeys).toEqual([]);
  });

  it("leaves every other pointer intact", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord({ deploymentId: "prod-1" }));
    await store.promote(makePromotion({ promotionId: "prod-1", builds: { web: "prod-1" } }));
    for (const p of ["pr-1", "pr-2"]) {
      await store.putStaged(makeRecord({ deploymentId: p }));
      await store.promote(makePromotion({ promotionId: p, builds: { web: p } }), p);
    }

    await store.removePointer("pr-1");

    expect(await store.pointerDeploymentId("web", "pr-1")).toBeUndefined();
    expect(await store.pointerDeploymentId("web", "pr-2")).toBe("pr-2");
    expect(await store.pointerDeploymentId("web")).toBe("prod-1");
  });
});

describe("pointer-scoped history", () => {
  it("returns only the given pointer's promotions, active marked per pointer", async () => {
    const store = storeStub();
    await store.putStaged(makeRecord({ deploymentId: "prod-1" }));
    await store.promote(makePromotion({ promotionId: "prod-1", builds: { web: "prod-1" } }));
    await store.putStaged(makeRecord({ deploymentId: "prev-1" }));
    await store.promote(
      makePromotion({ promotionId: "prev-1", ts: 2_000, builds: { web: "prev-1" } }),
      "staging",
    );

    expect(await store.history()).toEqual([
      { promotionId: "prod-1", ts: 1_000, builds: { web: "prod-1" }, active: true },
    ]);
    expect(await store.history("staging")).toEqual([
      { promotionId: "prev-1", ts: 2_000, builds: { web: "prev-1" }, active: true },
    ]);
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

describe("initialize / authorized", () => {
  it("seeds ownership and authenticates against the stored secret", async () => {
    const store = storeStub();
    expect(await store.authorized("s3cret")).toBe(false); // not seeded yet

    expect(await store.initialize("owner-1", "s3cret", false)).toEqual({
      ownerToken: "owner-1",
      secret: "s3cret",
    });

    expect(await store.authorized("s3cret")).toBe(true);
    expect(await store.authorized("wrong")).toBe(false);
  });

  it("returns the existing identity instead of re-seeding", async () => {
    const store = storeStub();
    await store.initialize("owner-1", "s3cret", false);

    expect(await store.initialize("owner-2", "other", false)).toEqual({
      ownerToken: "owner-1",
      secret: "s3cret",
    });

    expect(await store.authorized("s3cret")).toBe(true);
    expect(await store.authorized("other")).toBe(false);
  });

  it("converges a matching owner token onto the stored secret too", async () => {
    const store = storeStub();
    await store.initialize("owner-1", "old", false);

    expect(await store.initialize("owner-1", "new", false)).toEqual({
      ownerToken: "owner-1",
      secret: "old",
    });

    expect(await store.authorized("old")).toBe(true);
    expect(await store.authorized("new")).toBe(false);
  });

  it("adopts the presented identity when force is set", async () => {
    const store = storeStub();
    await store.initialize("owner-1", "s3cret", false);

    expect(await store.initialize("owner-2", "other", true)).toEqual({
      ownerToken: "owner-2",
      secret: "other",
    });

    expect(await store.authorized("other")).toBe(true);
    expect(await store.authorized("s3cret")).toBe(false);
  });
});

describe("destroy", () => {
  it("clears history, records, ownership and secret, and frees the slug", async () => {
    const store = storeStub();
    await store.initialize("owner-1", "s3cret", false);
    await store.putStaged(makeRecord());
    await store.promote(makePromotion());

    await store.destroy();

    expect(await store.history()).toEqual([]);
    expect(await store.record("web", "deploy-1")).toBeUndefined();
    expect(await store.authorized("s3cret")).toBe(false);

    await store.initialize("owner-2", "fresh", false);
    expect(await store.authorized("fresh")).toBe(true);
  });
});
