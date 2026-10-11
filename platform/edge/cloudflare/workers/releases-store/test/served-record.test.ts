import { createExecutionContext, env } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import type { Env } from "../src/env";
import type { PointerMove, ReleaseRecord } from "../src/store";

declare module "cloudflare:test" {
  interface ProvidedEnv extends Env {}
}

const WEB_PREFIX = "production/p1/web/r0a1b2c3d/isr";
const PREVIEW_PREFIX = "pr-42/p1/web/r0000000a/isr";

let stores = 0;

function storeStub() {
  return env.RELEASES_DO.get(env.RELEASES_DO.idFromName(`served-record-${stores++}`));
}

function makeRecord(over: Partial<ReleaseRecord> = {}): ReleaseRecord {
  return {
    app: "web",
    framework: "next",
    release: "build-1~fp1",
    buildId: "build-1",
    routeTable: { format: "next", table: { pathnames: [] } },
    functionUrls: { "/": "https://fn.example.com" },
    assetPrefix: "production/p1/web/r0a1b2c3d/assets",
    isrPrefix: WEB_PREFIX,
    purgeSecret: "purge-secret",
    createdAt: 1_000,
    ...over,
  };
}

function makePointerMove(over: Partial<PointerMove> = {}): PointerMove {
  return { promotionId: "promo-1", records: [makeRecord()], ...over };
}

describe("readServedRecord", () => {
  it("returns the record of a release a pointer serves, by its ISR prefix, on whichever pointer", async () => {
    const store = storeStub();
    const preview = makeRecord({ release: "build-2~fp2", isrPrefix: PREVIEW_PREFIX });
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
      isrPrefix: "production/p1/api/r0a1b2c3d/isr",
      purgeSecret: "api-secret",
    });
    await store.movePointer(makePointerMove({ records: [api, makeRecord()] }));

    expect(await store.readServedRecord(WEB_PREFIX)).toEqual({
      kind: "record",
      record: makeRecord(),
    });
    expect(await store.readServedRecord("production/p1/api/r0a1b2c3d/isr")).toEqual({
      kind: "record",
      record: api,
    });
  });

  it("does not take a release's name for its ISR prefix", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());

    expect(await store.readServedRecord("build-1~fp1")).toEqual({ kind: "not-served" });
  });

  it("returns not-served for a release no pointer serves", async () => {
    expect(await storeStub().readServedRecord(WEB_PREFIX)).toEqual({ kind: "not-served" });
  });

  it("stops returning a release once its pointer moves on", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());
    await store.movePointer(
      makePointerMove({
        promotionId: "promo-2",
        replaces: "promo-1",
        records: [makeRecord({ release: "build-2~fp2", isrPrefix: PREVIEW_PREFIX })],
      }),
    );

    expect(await store.readServedRecord(WEB_PREFIX)).toEqual({ kind: "not-served" });
    expect(await store.readServedRecord(PREVIEW_PREFIX)).toMatchObject({ kind: "record" });
  });

  it("stops returning a release once its pointer is removed", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove({ pointer: "pr-42" }));

    await store.removePointer("pr-42");

    expect(await store.readServedRecord(WEB_PREFIX)).toEqual({ kind: "not-served" });
  });

  it("is reachable through the entrypoint the entry worker binds, by project and ISR prefix", async () => {
    const slug = "served-record-entrypoint";
    await env.RELEASES_DO.get(env.RELEASES_DO.idFromName(slug)).movePointer(makePointerMove());
    const entry = new (await import("../src/index")).default(createExecutionContext(), env);

    expect(await entry.readServedRecord({ slug, isrPrefix: WEB_PREFIX })).toEqual({
      kind: "record",
      record: makeRecord(),
    });
    expect(await entry.readServedRecord({ slug, isrPrefix: PREVIEW_PREFIX })).toEqual({
      kind: "not-served",
    });
  });
});
