import { env, runInDurableObject } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import type { Env } from "../src/env";
import type { DeploymentRecord, PointerMove } from "../src/store";
import { ensureSchema, SCHEMA_VERSION } from "../src/store";

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
    identity: "deploy-1",
    deploymentId: "deploy-1",
    routingManifest: { pathnames: [] },
    functionUrls: { "/": "https://fn.example.com" },
    assetPrefix: "deploy-1",
    isrPrefix: "prod/p1/web/build-1",
    createdAt: 1_000,
    ...over,
  };
}

function makePointerMove(over: Partial<PointerMove> = {}): PointerMove {
  return {
    promotionId: "promo-1",
    records: [makeRecord()],
    ...over,
  };
}

describe("movePointer", () => {
  it("serves the records it carries on the pointer and names the promotion it serves", async () => {
    const store = storeStub();

    expect(await store.movePointer(makePointerMove())).toBe("moved");

    expect(await store.readServedPromotion()).toBe("promo-1");
    expect(await store.readPointerRecord("web")).toEqual({
      kind: "record",
      identity: "deploy-1",
      record: makeRecord(),
    });
  });

  it("replaces the promotion it names and serves the new one", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());

    const next = makePointerMove({
      promotionId: "promo-2",
      replaces: "promo-1",
      records: [makeRecord({ identity: "deploy-2" })],
    });
    expect(await store.movePointer(next)).toBe("moved");

    expect(await store.readServedPromotion()).toBe("promo-2");
    expect(await store.readPointerRecord("web")).toMatchObject({
      kind: "record",
      identity: "deploy-2",
    });
  });

  it("moves nothing when the pointer no longer serves the promotion it names", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());
    await store.movePointer(
      makePointerMove({
        promotionId: "promo-2",
        replaces: "promo-1",
        records: [makeRecord({ identity: "deploy-2" })],
      }),
    );

    const stale = makePointerMove({
      promotionId: "promo-3",
      replaces: "promo-1",
      records: [makeRecord({ identity: "deploy-3" })],
    });
    expect(await store.movePointer(stale)).toBe("stale");

    expect(await store.readServedPromotion()).toBe("promo-2");
    expect(await store.readPointerRecord("web")).toMatchObject({
      kind: "record",
      identity: "deploy-2",
    });
  });

  it("moves nothing when it names a promotion on a pointer that serves none", async () => {
    const store = storeStub();

    expect(await store.movePointer(makePointerMove({ replaces: "promo-0" }))).toBe("stale");

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

  it("moves only the pointer it names", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());

    await store.movePointer(
      makePointerMove({
        promotionId: "promo-preview",
        pointer: "pr-42",
        records: [makeRecord({ identity: "preview-1" })],
      }),
    );

    expect(await store.readServedPromotion()).toBe("promo-1");
    expect(await store.readServedPromotion("pr-42")).toBe("promo-preview");
    expect(await store.readPointerRecord("web")).toMatchObject({ identity: "deploy-1" });
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
      identity: "deploy-1",
    });
  });

  it("returns the served record when the known build is stale", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());

    expect(await store.readPointerRecord("web", "deploy-0")).toEqual({
      kind: "record",
      identity: "deploy-1",
      record: makeRecord(),
    });
  });

  it("resolves the pointer's sole app when no app is given", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());

    expect(await store.readPointerRecord()).toMatchObject({
      kind: "record",
      identity: "deploy-1",
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
        records: [makeRecord(), makeRecord({ app: "admin", identity: "admin-1" })],
        labels: [
          { label: "pr-42-web-aaaa", app: "web" },
          { label: "pr-42-admin-bbbb", app: "admin" },
        ],
      }),
    );

    expect(await store.readLabelRecord("pr-42-admin-bbbb")).toEqual({
      kind: "record",
      identity: "admin-1",
      record: makeRecord({ app: "admin", identity: "admin-1" }),
    });
    expect(await store.readLabelRecord("pr-42-web-aaaa", "deploy-1")).toEqual({
      kind: "unchanged",
      identity: "deploy-1",
    });
  });

  it("resolves a label with no app to the pointer's sole app", async () => {
    const store = storeStub();
    await store.movePointer(
      makePointerMove({ pointer: "pr-42", labels: [{ label: "pr-42-aaaa", app: "" }] }),
    );

    expect(await store.readLabelRecord("pr-42-aaaa")).toMatchObject({
      kind: "record",
      identity: "deploy-1",
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
        records: [makeRecord({ identity: "deploy-2" })],
        labels: [{ label: "new-bbbb", app: "web" }],
      }),
    );

    expect(await store.readLabelRecord("old-aaaa")).toEqual({ kind: "no-pointer" });
    expect(await store.readLabelRecord("new-bbbb")).toMatchObject({ identity: "deploy-2" });
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
        records: [makeRecord({ identity: "deploy-2" })],
        labels: [{ label: "alias-aaaa", app: "web" }],
      }),
    );

    expect(await store.readLabelRecord("alias-aaaa")).toMatchObject({ identity: "deploy-2" });
    expect(await store.readLabelRecord("first-bbbb")).toMatchObject({ identity: "deploy-1" });
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

describe("apps", () => {
  it("names every app ever moved, even one no pointer serves any more", async () => {
    const store = storeStub();
    await store.movePointer(
      makePointerMove({ records: [makeRecord(), makeRecord({ app: "admin" })] }),
    );
    await store.movePointer(makePointerMove({ promotionId: "promo-2", replaces: "promo-1" }));

    expect(await store.listApps()).toEqual(["admin", "web"]);
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
  it("seeds ownership and authenticates against the stored secret", async () => {
    const store = storeStub();
    expect(await store.authorized("s3cret")).toBe(false);

    expect(await store.initialize("owner-1", "s3cret", false)).toBe("adopted");

    expect(await store.authorized("s3cret")).toBe(true);
    expect(await store.authorized("wrong")).toBe(false);
  });

  it("keeps the existing identity instead of re-seeding, and hands nothing back", async () => {
    const store = storeStub();
    await store.initialize("owner-1", "s3cret", false);

    expect(await store.initialize("owner-2", "other", false)).toBe("refused");

    expect(await store.authorized("s3cret")).toBe(true);
    expect(await store.authorized("other")).toBe(false);
  });

  it("refuses a matching owner token too", async () => {
    const store = storeStub();
    await store.initialize("owner-1", "old", false);

    expect(await store.initialize("owner-1", "new", false)).toBe("refused");

    expect(await store.authorized("old")).toBe(true);
    expect(await store.authorized("new")).toBe(false);
  });

  it("adopts the presented identity when force is set", async () => {
    const store = storeStub();
    await store.initialize("owner-1", "s3cret", false);

    expect(await store.initialize("owner-2", "other", true)).toBe("adopted");

    expect(await store.authorized("other")).toBe(true);
    expect(await store.authorized("s3cret")).toBe(false);
  });
});

describe("destroy", () => {
  it("clears what it serves, ownership and secret, and frees the slug", async () => {
    const store = storeStub();
    await store.initialize("owner-1", "s3cret", false);
    await store.movePointer(makePointerMove());

    await store.destroy();

    expect(await store.readServedPromotion()).toBeUndefined();
    expect(await store.readPointerRecord("web")).toEqual({ kind: "no-pointer" });
    expect(await store.listApps()).toEqual([]);
    expect(await store.authorized("s3cret")).toBe(false);

    await store.initialize("owner-2", "fresh", false);
    expect(await store.authorized("fresh")).toBe(true);
  });
});

describe("ensureSchema", () => {
  it("drops a superseded schema's tables and keeps the store's identity", async () => {
    const stub = env.DEPLOYMENTS_DO.get(env.DEPLOYMENTS_DO.idFromName("legacy"));
    await runInDurableObject(stub, (_instance, ctx) => {
      const storage = ctx.storage;
      for (const table of ["records", "promotions", "pointers", "served", "apps"]) {
        storage.sql.exec(`DROP TABLE IF EXISTS ${table}`);
      }
      storage.sql.exec(
        `CREATE TABLE records (
           app TEXT NOT NULL,
           identity TEXT NOT NULL,
           data TEXT NOT NULL,
           PRIMARY KEY (app, identity)
         );
         CREATE TABLE promotions (
           promotion_id TEXT PRIMARY KEY,
           ts INTEGER NOT NULL,
           builds TEXT NOT NULL,
           seq INTEGER NOT NULL,
           tag TEXT,
           pointer TEXT NOT NULL DEFAULT '@production'
         );
         CREATE TABLE pointers (
           name TEXT PRIMARY KEY,
           promotion_id TEXT NOT NULL
         );`,
      );
      storage.sql.exec(
        `INSERT INTO records (app, identity, data) VALUES (?, ?, ?)`,
        "web",
        "deploy-1",
        JSON.stringify(makeRecord()),
      );
      storage.sql.exec(
        `INSERT INTO pointers (name, promotion_id) VALUES (?, ?)`,
        "@production",
        "promo-1",
      );
      storage.sql.exec(
        `INSERT INTO meta (key, value) VALUES (?, ?)
         ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
        "secret",
        "s3cret",
      );
      storage.sql.exec(
        `INSERT INTO meta (key, value) VALUES (?, ?)
         ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
        "schemaVersion",
        "2",
      );

      ensureSchema(storage);

      const tables = storage.sql
        .exec<{ name: string }>(`SELECT name FROM sqlite_master WHERE type = 'table'`)
        .toArray()
        .map((t) => t.name);
      expect(tables).not.toContain("records");
      expect(tables).not.toContain("promotions");
      const count = (table: string) =>
        storage.sql.exec<{ n: number }>(`SELECT COUNT(*) AS n FROM ${table}`).one().n;
      expect(count("pointers")).toBe(0);
      expect(count("served")).toBe(0);

      const meta = (key: string) =>
        storage.sql
          .exec<{ value: string }>(`SELECT value FROM meta WHERE key = ?`, key)
          .toArray()[0]?.value;
      expect(meta("secret")).toBe("s3cret");
      expect(meta("schemaVersion")).toBe(String(SCHEMA_VERSION));
    });
  });

  it("adds the labels table to a store that predates it and keeps the pointers it serves", async () => {
    const store = storeStub();
    await store.initialize("owner-1", "s3cret", false);
    await store.movePointer(makePointerMove());

    await runInDurableObject(storeStub(), (_instance, ctx) => {
      ctx.storage.sql.exec(`DROP TABLE labels`);
      ctx.storage.sql.exec(`UPDATE meta SET value = '3' WHERE key = 'schemaVersion'`);
      ensureSchema(ctx.storage);
      const tables = ctx.storage.sql
        .exec<{ name: string }>(`SELECT name FROM sqlite_master WHERE type = 'table'`)
        .toArray()
        .map((t) => t.name);
      expect(tables).toContain("labels");
    });

    expect(await store.readPointerRecord("web")).toMatchObject({
      kind: "record",
      identity: "deploy-1",
    });
  });

  it("leaves a current schema's rows alone", async () => {
    const store = storeStub();
    await store.initialize("owner-1", "s3cret", false);
    await store.movePointer(makePointerMove());

    await runInDurableObject(storeStub(), (_instance, ctx) => {
      ensureSchema(ctx.storage);
    });

    expect(await store.readPointerRecord("web")).toMatchObject({
      kind: "record",
      identity: "deploy-1",
    });
    expect(await store.authorized("s3cret")).toBe(true);
  });
});
