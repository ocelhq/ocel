import { createExecutionContext, env } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import type { Env } from "../src/env";
import type { PointerMove, ReleaseRecord } from "../src/store";

declare module "cloudflare:test" {
  interface ProvidedEnv extends Env {}
}

let stores = 0;

function storeStub() {
  return env.RELEASES_DO.get(env.RELEASES_DO.idFromName(`served-record-${stores++}`));
}

function makeRecord(over: Partial<ReleaseRecord> = {}): ReleaseRecord {
  return {
    app: "web",
    framework: "next",
    release: "deploy-1",
    buildId: "deploy-1",
    routeTable: { format: "next", table: { pathnames: [] } },
    functionUrls: { "/": "https://fn.example.com" },
    assetPrefix: "deploy-1",
    isrPrefix: "prod/p1/web/build-1",
    purgeSecret: "purge-secret",
    createdAt: 1_000,
    ...over,
  };
}

function makePointerMove(over: Partial<PointerMove> = {}): PointerMove {
  return { promotionId: "promo-1", records: [makeRecord()], ...over };
}

describe("readServedRecord", () => {
  it("returns the record of a release a pointer serves, on whichever pointer", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());
    await store.movePointer(
      makePointerMove({
        promotionId: "promo-preview",
        pointer: "pr-42",
        records: [makeRecord({ release: "preview-1" })],
      }),
    );

    expect(await store.readServedRecord("deploy-1")).toEqual({
      kind: "record",
      record: makeRecord(),
    });
    expect(await store.readServedRecord("preview-1")).toEqual({
      kind: "record",
      record: makeRecord({ release: "preview-1" }),
    });
  });

  it("returns not-served for a release no pointer serves", async () => {
    expect(await storeStub().readServedRecord("deploy-1")).toEqual({ kind: "not-served" });
  });

  it("stops returning a release once its pointer moves on", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove());
    await store.movePointer(
      makePointerMove({
        promotionId: "promo-2",
        replaces: "promo-1",
        records: [makeRecord({ release: "deploy-2" })],
      }),
    );

    expect(await store.readServedRecord("deploy-1")).toEqual({ kind: "not-served" });
    expect(await store.readServedRecord("deploy-2")).toMatchObject({ kind: "record" });
  });

  it("stops returning a release once its pointer is removed", async () => {
    const store = storeStub();
    await store.movePointer(makePointerMove({ pointer: "pr-42" }));

    await store.removePointer("pr-42");

    expect(await store.readServedRecord("deploy-1")).toEqual({ kind: "not-served" });
  });

  it("is reachable through the entrypoint the entry worker binds, by project and release", async () => {
    const slug = "served-record-entrypoint";
    await env.RELEASES_DO.get(env.RELEASES_DO.idFromName(slug)).movePointer(makePointerMove());
    const entry = new (await import("../src/index")).default(createExecutionContext(), env);

    expect(await entry.readServedRecord({ slug, release: "deploy-1" })).toEqual({
      kind: "record",
      record: makeRecord(),
    });
    expect(await entry.readServedRecord({ slug, release: "deploy-9" })).toEqual({
      kind: "not-served",
    });
  });
});
