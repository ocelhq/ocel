import { env, runInDurableObject } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import type { Env } from "../src/env";
import type { KeptRelease, PointerMove, StoredRecord } from "../src/store";
import { ensureSchema } from "../src/store";

declare module "cloudflare:test" {
  interface ProvidedEnv extends Env {}
}

function storeStub() {
  const id = env.RELEASES_DO.idFromName("acme-web");
  return env.RELEASES_DO.get(id);
}

function makeRecord(over: Partial<StoredRecord> = {}): StoredRecord {
  return {
    app: "web",
    framework: "next",
    release: "deploy-1",
    buildId: "deploy-1",
    rootFunction: "/",
    routeTable: { format: "next", key: "prod/p1/web/route-table.json" },
    functionUrls: { "/": "https://fn.example.com" },
    storageCoordinate: "p1/prod/web/r00000b01",
    createdAt: 1_000,
    ...over,
  };
}

function makePointerMove(over: Partial<PointerMove> = {}): PointerMove {
  return {
    promotionId: "promo-1",
    records: [makeRecord()],
    kept: [],
    ...over,
  };
}

describe("movePointer", () => {
  it("serves the records it carries on the pointer and names the promotion it serves", async () => {
    const store = storeStub();

    expect(await store.movePointer(makePointerMove())).toEqual({ kind: "moved" });

    expect(await store.readServedPromotion()).toBe("promo-1");
    expect(await store.readPointerRecord("web")).toEqual({
      kind: "record",
      release: "deploy-1",
      record: makeRecord(),
    });
  });

  it("refuses a record that locates none of its objects, and moves nothing", async () => {
    const store = storeStub();

    expect(
      await store.movePointer(
        makePointerMove({ records: [makeRecord({ storageCoordinate: "p1/prod/web" })] }),
      ),
    ).toEqual({ kind: "unlocated", app: "web", release: "deploy-1" });

    expect(await store.readServedPromotion()).toBeUndefined();
  });

  it("replaces the promotion it names and serves the new one", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());

    const next = makePointerMove({
      promotionId: "promo-2",
      replaces: "promo-1",
      records: [makeRecord({ release: "deploy-2" })],
    });
    expect(await store.movePointer(next)).toEqual({ kind: "moved" });

    expect(await store.readServedPromotion()).toBe("promo-2");
    expect(await store.readPointerRecord("web")).toMatchObject({
      kind: "record",
      release: "deploy-2",
    });
  });

  it("moves nothing when the pointer no longer serves the promotion it names", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());
    await store.movePointer(
      makePointerMove({
        promotionId: "promo-2",
        replaces: "promo-1",
        records: [makeRecord({ release: "deploy-2" })],
      }),
    );

    const stale = makePointerMove({
      promotionId: "promo-3",
      replaces: "promo-1",
      records: [makeRecord({ release: "deploy-3" })],
    });
    expect(await store.movePointer(stale)).toEqual({ kind: "stale" });

    expect(await store.readServedPromotion()).toBe("promo-2");
    expect(await store.readPointerRecord("web")).toMatchObject({
      kind: "record",
      release: "deploy-2",
    });
  });

  it("moves nothing when it names a promotion on a pointer that serves none", async () => {
    const store = storeStub();

    expect(await store.movePointer(makePointerMove({ replaces: "promo-0" }))).toEqual({
      kind: "stale",
    });

    expect(await store.readServedPromotion()).toBeUndefined();
    expect(await store.readPointerRecord("web")).toEqual({ kind: "no-pointer" });
  });

  it("stops serving an app the new promotion leaves out", async () => {
    const store = storeStub();
    await store.movePointer(
      makePointerMove({ records: [makeRecord(), makeRecord({ app: "admin" })] }),
    );

    await store.movePointer(makePointerMove({ promotionId: "promo-2", replaces: "promo-1" }));

    expect(await store.readPointerRecord("admin")).toEqual({ kind: "no-pointer" });
  });

  it("refuses a different record for a release the pointer serves, and moves nothing", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());

    const changed = makePointerMove({
      promotionId: "promo-2",
      replaces: "promo-1",
      records: [makeRecord({ functionUrls: { "/": "https://other.example.com" } })],
    });
    expect(await store.movePointer(changed)).toEqual({
      kind: "different-record",
      app: "web",
      release: "deploy-1",
    });

    expect(await store.readServedPromotion()).toBe("promo-1");
    expect(await store.readPointerRecord("web")).toEqual({
      kind: "record",
      release: "deploy-1",
      record: makeRecord(),
    });
  });

  it("refuses a different record for a release another pointer serves", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());

    const preview = makePointerMove({
      pointer: "pr-42",
      promotionId: "promo-preview",
      records: [makeRecord({ createdAt: 2_000 })],
    });
    expect(await store.movePointer(preview)).toEqual({
      kind: "different-record",
      app: "web",
      release: "deploy-1",
    });

    expect(await store.readServedPromotion("pr-42")).toBeUndefined();
  });

  it("serves a release again with the edge payload of the later move", async () => {
    const store = storeStub();
    await store.movePointer(
      makePointerMove({ records: [makeRecord({ edge: { envelope: "a" } })] }),
    );

    const rewrapped = makePointerMove({
      promotionId: "promo-2",
      replaces: "promo-1",
      records: [makeRecord({ edge: { envelope: "b" } })],
    });
    expect(await store.movePointer(rewrapped)).toEqual({ kind: "moved" });

    expect(await store.readPointerRecord("web")).toMatchObject({
      kind: "record",
      record: { edge: { envelope: "b" } },
    });
  });

  it("takes the same release of another app for that app's own", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());

    const admin = makePointerMove({
      pointer: "pr-42",
      promotionId: "promo-preview",
      records: [makeRecord({ app: "admin", storageCoordinate: "p1/prod/admin/r00000b01" })],
    });
    expect(await store.movePointer(admin)).toEqual({ kind: "moved" });
  });

  it("moves only the pointer it names", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());

    await store.movePointer(
      makePointerMove({
        promotionId: "promo-preview",
        pointer: "pr-42",
        records: [makeRecord({ release: "preview-1" })],
      }),
    );

    expect(await store.readServedPromotion()).toBe("promo-1");
    expect(await store.readServedPromotion("pr-42")).toBe("promo-preview");
    expect(await store.readPointerRecord("web")).toMatchObject({ release: "deploy-1" });
  });
});

describe("readPointerRecord", () => {
  it("returns no-pointer when nothing is served on the pointer", async () => {
    expect(await storeStub().readPointerRecord("web")).toEqual({ kind: "no-pointer" });
  });

  it("omits the record when the known build is still served", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());

    expect(await store.readPointerRecord("web", "deploy-1")).toEqual({
      kind: "unchanged",
      release: "deploy-1",
    });
  });

  it("returns the served record when the known build is stale", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());

    expect(await store.readPointerRecord("web", "deploy-0")).toEqual({
      kind: "record",
      release: "deploy-1",
      record: makeRecord(),
    });
  });

  it("resolves the pointer's sole app when no app is given", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());

    expect(await store.readPointerRecord()).toMatchObject({
      kind: "record",
      release: "deploy-1",
    });
  });

  it("returns ambiguous-app when the pointer serves more than one app", async () => {
    const store = storeStub();
    await store.movePointer(
      makePointerMove({ records: [makeRecord(), makeRecord({ app: "admin" })] }),
    );

    expect(await store.readPointerRecord()).toEqual({ kind: "ambiguous-app" });
  });

  it("returns no-pointer for an app the pointer does not serve", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());

    expect(await store.readPointerRecord("admin")).toEqual({ kind: "no-pointer" });
  });
});

describe("readLabelRecord", () => {
  it("resolves a label to the record its pointer serves for the label's app", async () => {
    const store = storeStub();
    await store.movePointer(
      makePointerMove({
        pointer: "pr-42",
        records: [makeRecord(), makeRecord({ app: "admin", release: "admin-1" })],
        labels: [
          { label: "pr-42-web-aaaa", app: "web" },
          { label: "pr-42-admin-bbbb", app: "admin" },
        ],
      }),
    );

    expect(await store.readLabelRecord("pr-42-admin-bbbb")).toEqual({
      kind: "record",
      release: "admin-1",
      record: makeRecord({ app: "admin", release: "admin-1" }),
    });
    expect(await store.readLabelRecord("pr-42-web-aaaa", "deploy-1")).toEqual({
      kind: "unchanged",
      release: "deploy-1",
    });
  });

  it("resolves a label with no app to the pointer's sole app", async () => {
    const store = storeStub();
    await store.movePointer(
      makePointerMove({ pointer: "pr-42", labels: [{ label: "pr-42-aaaa", app: "" }] }),
    );

    expect(await store.readLabelRecord("pr-42-aaaa")).toMatchObject({
      kind: "record",
      release: "deploy-1",
    });
  });

  it("returns no-pointer for a label no move carried", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove({ pointer: "pr-42" }));

    expect(await store.readLabelRecord("pr-42-aaaa")).toEqual({ kind: "no-pointer" });
  });

  it("stops serving the labels a later move of the pointer leaves out", async () => {
    const store = storeStub();
    await store.movePointer(
      makePointerMove({ pointer: "pr-42", labels: [{ label: "old-aaaa", app: "web" }] }),
    );

    await store.movePointer(
      makePointerMove({
        pointer: "pr-42",
        promotionId: "promo-2",
        replaces: "promo-1",
        records: [makeRecord({ release: "deploy-2" })],
        labels: [{ label: "new-bbbb", app: "web" }],
      }),
    );

    expect(await store.readLabelRecord("old-aaaa")).toEqual({ kind: "no-pointer" });
    expect(await store.readLabelRecord("new-bbbb")).toMatchObject({ release: "deploy-2" });
  });

  it("keeps serving a deployment pointer's label after the alias moves on", async () => {
    const store = storeStub();
    await store.movePointer(
      makePointerMove({ pointer: "pr-42", labels: [{ label: "alias-aaaa", app: "web" }] }),
    );
    await store.movePointer(
      makePointerMove({
        pointer: "pr-42@promo-1",
        labels: [{ label: "first-bbbb", app: "web" }],
      }),
    );
    await store.movePointer(
      makePointerMove({
        pointer: "pr-42",
        promotionId: "promo-2",
        replaces: "promo-1",
        records: [makeRecord({ release: "deploy-2" })],
        labels: [{ label: "alias-aaaa", app: "web" }],
      }),
    );

    expect(await store.readLabelRecord("alias-aaaa")).toMatchObject({ release: "deploy-2" });
    expect(await store.readLabelRecord("first-bbbb")).toMatchObject({ release: "deploy-1" });
  });
});

describe("removePointer", () => {
  it("leaves nothing served on the pointer and every other pointer as it was", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());
    await store.movePointer(
      makePointerMove({
        promotionId: "promo-preview",
        pointer: "pr-42",
        labels: [{ label: "pr-42-aaaa", app: "web" }],
      }),
    );

    await store.removePointer("pr-42");

    expect(await store.readLabelRecord("pr-42-aaaa")).toEqual({ kind: "no-pointer" });

    expect(await store.readServedPromotion("pr-42")).toBeUndefined();
    expect(await store.readServedPromotion()).toBe("promo-1");
  });

  it("removing a pointer that serves nothing is a clean no-op", async () => {
    await storeStub().removePointer("never-moved");
  });
});

describe("the builds a pointer served before", () => {
  const token = (n: number) => `r${String(n).padStart(8, "0")}`;

  const build = (n: number, over: Partial<StoredRecord> = {}) =>
    makeRecord({
      release: `deploy-${n}`,
      buildId: `build-${n}`,
      storageCoordinate: `p1/prod/web/${token(n)}`,
      ...over,
    });

  const keptThrough = (n: number): KeptRelease[] =>
    Array.from({ length: n }, (_, i) => ({ app: "web", releaseToken: token(i + 1) }));

  async function promote(
    store: ReturnType<typeof storeStub>,
    records: StoredRecord[],
    n: number,
    kept: KeptRelease[] = keptThrough(n),
  ) {
    await store.movePointer(
      makePointerMove({
        promotionId: `promo-${n}`,
        replaces: n === 1 ? undefined : `promo-${n - 1}`,
        records,
        kept,
      }),
    );
  }

  it("serves a record naming the storage coordinate of each build its app served on the pointer before", async () => {
    const store = storeStub();
    await promote(store, [build(1)], 1);
    await promote(store, [build(2)], 2);
    await promote(store, [build(3)], 3);

    const served = await store.readPointerRecord("web");

    expect(served).toMatchObject({
      kind: "record",
      record: {
        buildId: "build-3",
        buildCoordinates: {
          "build-1": "p1/prod/web/r00000001",
          "build-2": "p1/prod/web/r00000002",
        },
      },
    });
  });

  it("names no build of another app or another pointer", async () => {
    const store = storeStub();
    await promote(
      store,
      [build(1), build(1, { app: "admin", storageCoordinate: "p1/prod/admin/r00000001" })],
      1,
    );
    await store.movePointer(
      makePointerMove({ pointer: "pr-42", promotionId: "promo-pr", records: [build(9)] }),
    );
    await promote(store, [build(2)], 2);

    const served = await store.readPointerRecord("web");

    expect(served).toMatchObject({
      kind: "record",
      record: { buildCoordinates: { "build-1": "p1/prod/web/r00000001" } },
    });
  });

  it("names the build of every release the ledger still keeps on the pointer, however many builds came after it", async () => {
    const store = storeStub();
    for (let n = 1; n <= 23; n++) await promote(store, [build(n)], n);

    const served = await store.readPointerRecord("web");
    const named = served.kind === "record" ? Object.keys(served.record.buildCoordinates ?? {}) : [];

    expect(named).toHaveLength(22);
    expect(named).toContain("build-1");
  });

  it("names no build of a release the ledger no longer keeps on the pointer", async () => {
    const store = storeStub();
    await promote(store, [build(1)], 1);
    await promote(store, [build(2)], 2);
    await promote(store, [build(3)], 3, keptThrough(3).slice(1));

    const served = await store.readPointerRecord("web");

    expect(served).toMatchObject({
      kind: "record",
      record: { buildCoordinates: { "build-2": "p1/prod/web/r00000002" } },
    });
    expect(served.kind === "record" && served.record.buildCoordinates).not.toHaveProperty(
      "build-1",
    );
  });

  it("lists the storage coordinate of every other release of an app it serves or keeps, on any pointer", async () => {
    const store = storeStub();
    await promote(store, [build(1)], 1);
    await promote(store, [build(2)], 2);
    await store.movePointer(
      makePointerMove({ pointer: "pr-42", promotionId: "promo-pr", records: [build(9)] }),
    );
    await promote(
      store,
      [build(3), build(3, { app: "admin", storageCoordinate: "p1/prod/admin/r00000003" })],
      3,
    );

    const listed = await store.listOtherAppReleases("p1/prod/web/r00000003");

    expect([...listed].sort()).toEqual([
      "p1/prod/web/r00000001",
      "p1/prod/web/r00000002",
      "p1/prod/web/r00000009",
    ]);
  });

  it("forgets the builds of a pointer it removes", async () => {
    const store = storeStub();
    const preview = (n: number, replaces?: string) =>
      makePointerMove({
        pointer: "pr-42",
        promotionId: `promo-pr-${n}`,
        replaces,
        records: [build(n)],
        labels: [{ label: "pr-42-aaaa", app: "web" }],
      });
    await store.movePointer(preview(1));
    await store.removePointer("pr-42");
    await store.movePointer(preview(2));

    const served = await store.readLabelRecord("pr-42-aaaa");

    expect(served).toMatchObject({ kind: "record", record: { buildId: "build-2" } });
    expect(served.kind === "record" && served.record.buildCoordinates).toBeFalsy();
  });
});

describe("version stamp", () => {
  it("is readable and updatable", async () => {
    const store = storeStub();
    expect(await store.readVersionStamp()).toBeUndefined();

    await store.setVersionStamp("v1");
    expect(await store.readVersionStamp()).toBe("v1");

    await store.setVersionStamp("v2");
    expect(await store.readVersionStamp()).toBe("v2");
  });
});

describe("initialize / authorized", () => {
  it("seeds the secret and authenticates against it", async () => {
    const store = storeStub();
    expect(await store.authorized("s3cret")).toBe(false);

    expect(await store.initialize("s3cret")).toBe("adopted");

    expect(await store.authorized("s3cret")).toBe(true);
    expect(await store.authorized("wrong")).toBe(false);
  });

  it("keeps the existing identity instead of re-seeding, and hands nothing back", async () => {
    const store = storeStub();
    await store.initialize("s3cret");

    expect(await store.initialize("other")).toBe("refused");

    expect(await store.authorized("s3cret")).toBe(true);
    expect(await store.authorized("other")).toBe(false);
  });

  it("adopts a retried initialize that offers the secret it already holds", async () => {
    const store = storeStub();
    await store.initialize("s3cret");

    expect(await store.initialize("s3cret")).toBe("adopted");

    expect(await store.authorized("s3cret")).toBe(true);
  });

  it("creates no storage for a slug it only authenticates against", async () => {
    const stranger = env.RELEASES_DO.get(env.RELEASES_DO.idFromName("never-initialized"));

    expect(await stranger.authorized("x")).toBe(false);

    await runInDurableObject(stranger, (_instance, ctx) => {
      const tables = ctx.storage.sql
        .exec(
          `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE '\\_%' ESCAPE '\\'`,
        )
        .toArray();
      expect(tables).toEqual([]);
    });
  });
});

describe("destroy", () => {
  it("clears what it serves and its secret, and frees the slug", async () => {
    const store = storeStub();
    await store.initialize("s3cret");
    await store.movePointer(makePointerMove());

    await store.destroy();

    expect(await store.readServedPromotion()).toBeUndefined();
    expect(await store.readPointerRecord("web")).toEqual({ kind: "no-pointer" });
    expect(await store.authorized("s3cret")).toBe(false);

    await store.initialize("fresh");
    expect(await store.authorized("fresh")).toBe(true);
  });
});

describe("ensureSchema", () => {
  it("keeps every pointer it serves whatever schema version the store recorded", async () => {
    const store = storeStub();
    await store.initialize("s3cret");
    await store.movePointer(makePointerMove());

    await runInDurableObject(storeStub(), (_instance, ctx) => {
      ctx.storage.sql.exec(
        `INSERT INTO meta (key, value) VALUES ('schemaVersion', '1')
         ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
      );
      ensureSchema(ctx.storage);
    });

    expect(await store.readPointerRecord("web")).toMatchObject({
      kind: "record",
      release: "deploy-1",
    });
    expect(await store.authorized("s3cret")).toBe(true);
  });

  it("leaves a current schema's rows alone", async () => {
    const store = storeStub();
    await store.initialize("s3cret");
    await store.movePointer(makePointerMove());

    await runInDurableObject(storeStub(), (_instance, ctx) => {
      ensureSchema(ctx.storage);
    });

    expect(await store.readPointerRecord("web")).toMatchObject({
      kind: "record",
      release: "deploy-1",
    });
    expect(await store.authorized("s3cret")).toBe(true);
  });
});
