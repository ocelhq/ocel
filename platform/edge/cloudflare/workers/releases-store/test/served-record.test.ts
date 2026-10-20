import { createExecutionContext, env } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import type { Env } from "../src/env";
import type { PointerMove, StoredRecord } from "../src/store";

declare module "cloudflare:test" {
  interface ProvidedEnv extends Env {}
}

const WEB_PREFIX = "pages/p1/prod/web/r0a1b2c3d";
const PREVIEW_PREFIX = "pages/p1/pr-42/web/r0000000a";
const OLDER_PREFIX = "pages/p1/prod/web/r00000001";
const NEWER_PREFIX = "pages/p1/prod/web/r00000002";
const WEB_KEPT = { app: "web", releaseToken: "r0a1b2c3d" };
const OLDER_KEPT = { app: "web", releaseToken: "r00000001" };
const NEWER_KEPT = { app: "web", releaseToken: "r00000002" };

let stores = 0;

function storeStub() {
  return env.RELEASES_DO.get(env.RELEASES_DO.idFromName(`served-record-${stores++}`));
}

function makeRecord(over: Partial<StoredRecord> = {}): StoredRecord {
  return {
    app: "web",
    framework: "next",
    release: "build-1~fp1",
    buildId: "build-1",
    rootFunction: "/",
    routeTable: { format: "next", key: "prod/p1/web/route-table.json" },
    functionUrls: { "/": "https://fn.example.com" },
    assetPrefix: "assets/p1/prod/web/r0a1b2c3d",
    pagesPrefix: WEB_PREFIX,
    edge: { purgeSecret: "purge-secret" },
    createdAt: 1_000,
    ...over,
  };
}

function makePointerMove(over: Partial<PointerMove> = {}): PointerMove {
  return { promotionId: "promo-1", records: [makeRecord()], kept: [], ...over };
}

describe("readServedRecord", () => {
  it("returns the record of a release a pointer serves, by its pages prefix, on whichever pointer", async () => {
    const store = storeStub();
    const preview = makeRecord({ release: "build-2~fp2", pagesPrefix: PREVIEW_PREFIX });
    await store.movePointer(makePointerMove());
    await store.movePointer(
      makePointerMove({ promotionId: "promo-preview", pointer: "pr-42", records: [preview] }),
    );

    expect(await store.readServedRecord(WEB_PREFIX)).toEqual({
      kind: "record",
      record: makeRecord(),
    });
    expect(await store.readServedRecord(PREVIEW_PREFIX)).toEqual({
      kind: "record",
      record: preview,
    });
  });

  it("returns each app its own record when two apps' releases share a release token", async () => {
    const store = storeStub();
    const api = makeRecord({
      app: "api",
      pagesPrefix: "pages/p1/prod/api/r0a1b2c3d",
      edge: { purgeSecret: "api-secret" },
    });
    await store.movePointer(makePointerMove({ records: [api, makeRecord()] }));

    expect(await store.readServedRecord(WEB_PREFIX)).toEqual({
      kind: "record",
      record: makeRecord(),
    });
    expect(await store.readServedRecord("pages/p1/prod/api/r0a1b2c3d")).toEqual({
      kind: "record",
      record: api,
    });
  });

  it("does not take a release's name for its pages prefix", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());

    expect(await store.readServedRecord("build-1~fp1")).toEqual({ kind: "not-served" });
  });

  it("answers forgotten for a release no pointer serves or keeps", async () => {
    expect(await storeStub().readServedRecord(WEB_PREFIX)).toEqual({ kind: "forgotten" });
  });

  it("answers not-served for a release no pointer serves but one still keeps", async () => {
    const store = storeStub();
    await store.movePointer(
      makePointerMove({
        records: [makeRecord({ release: "build-2~fp2", pagesPrefix: OLDER_PREFIX })],
        kept: [WEB_KEPT, OLDER_KEPT],
      }),
    );

    expect(await store.readServedRecord(WEB_PREFIX)).toEqual({ kind: "not-served" });
  });

  it("knows a kept release by its app as well as its release token", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove({ kept: [WEB_KEPT] }));

    expect(await store.readServedRecord("pages/p1/prod/api/r0a1b2c3d")).toEqual({
      kind: "forgotten",
    });
  });

  it("forgets a release the next move on its pointer no longer keeps", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove({ kept: [WEB_KEPT, OLDER_KEPT] }));
    await store.movePointer(
      makePointerMove({
        promotionId: "promo-2",
        replaces: "promo-1",
        records: [makeRecord({ release: "build-2~fp2", pagesPrefix: NEWER_PREFIX })],
        kept: [NEWER_KEPT, WEB_KEPT],
      }),
    );

    expect(await store.readServedRecord(OLDER_PREFIX)).toEqual({ kind: "forgotten" });
    expect(await store.readServedRecord(WEB_PREFIX)).toEqual({ kind: "not-served" });
  });

  it("keeps a release while another pointer still keeps it", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove({ kept: [WEB_KEPT] }));
    await store.movePointer(
      makePointerMove({ promotionId: "promo-preview", pointer: "pr-42", kept: [WEB_KEPT] }),
    );
    await store.movePointer(
      makePointerMove({
        promotionId: "promo-2",
        replaces: "promo-1",
        records: [makeRecord({ release: "build-2~fp2", pagesPrefix: NEWER_PREFIX })],
        kept: [NEWER_KEPT],
      }),
    );
    await store.removePointer("pr-42");

    expect(await store.readServedRecord(WEB_PREFIX)).toEqual({ kind: "forgotten" });
  });

  it("forgets every release a removed pointer kept", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove({ pointer: "pr-42", kept: [WEB_KEPT, OLDER_KEPT] }));

    await store.removePointer("pr-42");

    expect(await store.readServedRecord(WEB_PREFIX)).toEqual({ kind: "forgotten" });
    expect(await store.readServedRecord(OLDER_PREFIX)).toEqual({ kind: "forgotten" });
  });

  it("forgets every release once the store is destroyed", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove({ kept: [WEB_KEPT, OLDER_KEPT] }));

    await store.destroy();

    expect(await store.readServedRecord(OLDER_PREFIX)).toEqual({ kind: "forgotten" });
  });

  it("keeps the releases a move kept when the move is stale", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove({ kept: [WEB_KEPT, OLDER_KEPT] }));

    await store.movePointer(
      makePointerMove({ promotionId: "promo-3", replaces: "promo-2", kept: [NEWER_KEPT] }),
    );

    expect(await store.readServedRecord(OLDER_PREFIX)).toEqual({ kind: "not-served" });
  });

  it("stops returning a release once its pointer moves on", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());
    await store.movePointer(
      makePointerMove({
        promotionId: "promo-2",
        replaces: "promo-1",
        records: [makeRecord({ release: "build-2~fp2", pagesPrefix: PREVIEW_PREFIX })],
        kept: [WEB_KEPT],
      }),
    );

    expect(await store.readServedRecord(WEB_PREFIX)).toEqual({ kind: "not-served" });
    expect(await store.readServedRecord(PREVIEW_PREFIX)).toMatchObject({ kind: "record" });
  });

  it("is reachable through the entrypoint the entry worker binds, by project and pages prefix", async () => {
    const slug = "served-record-entrypoint";
    await env.RELEASES_DO.get(env.RELEASES_DO.idFromName(slug)).movePointer(makePointerMove());
    const entry = new (await import("../src/index")).default(createExecutionContext(), env);

    expect(await entry.readServedRecord({ slug, pagesPrefix: WEB_PREFIX })).toEqual({
      kind: "record",
      record: makeRecord(),
    });
    expect(await entry.readServedRecord({ slug, pagesPrefix: PREVIEW_PREFIX })).toEqual({
      kind: "forgotten",
    });
  });
});
