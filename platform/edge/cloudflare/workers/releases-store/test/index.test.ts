import { createExecutionContext, env, SELF } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import type { Env } from "../src/env";
import type { ReleaseRecord } from "../src/store";

declare module "cloudflare:test" {
  interface ProvidedEnv extends Env {}
}

const BOOTSTRAP = "dev-secret"; // matches wrangler.jsonc's vars.BOOTSTRAP_SECRET
const SLUG = "acme-web";
const SECRET = "project-secret"; // per-project secret seeded via /initialize

function req(path: string, init: RequestInit = {}) {
  return new Request(`https://store.example${path}`, init);
}

function bearerReq(path: string, token: string, init: RequestInit = {}) {
  return req(path, {
    ...init,
    headers: { ...init.headers, authorization: `Bearer ${token}` },
  });
}

async function initialize(slug = SLUG, secret = SECRET) {
  return SELF.fetch(
    bearerReq(`/${slug}/initialize`, BOOTSTRAP, {
      method: "POST",
      body: JSON.stringify({ ownerToken: "owner-1", secret }),
    }),
  );
}

function authedReq(path: string, init: RequestInit = {}) {
  return bearerReq(`/${SLUG}${path}`, SECRET, init);
}

function makeRecord(over: Partial<ReleaseRecord> = {}): ReleaseRecord {
  return {
    app: "web",
    framework: "next",
    release: "deploy-1",
    buildId: "deploy-1",
    routingManifest: { pathnames: [] },
    functionUrls: { "/": "https://fn.example.com" },
    assetPrefix: "deploy-1",
    isrPrefix: "prod/p1/web/build-1",
    createdAt: 1_000,
    ...over,
  };
}

function pointerMoveBody(over: Record<string, unknown> = {}) {
  return JSON.stringify({ promotionId: "promo-1", records: [makeRecord()], ...over });
}

async function movePointer(over: Record<string, unknown> = {}) {
  return SELF.fetch(authedReq("/move-pointer", { method: "POST", body: pointerMoveBody(over) }));
}

describe("initialize", () => {
  it("rejects an initialize signed with the wrong bootstrap credential", async () => {
    const res = await SELF.fetch(
      bearerReq(`/${SLUG}/initialize`, "wrong", {
        method: "POST",
        body: JSON.stringify({ ownerToken: "owner-1", secret: SECRET }),
      }),
    );
    expect(res.status).toBe(401);
  });

  it("seeds the instance and reports nothing but that it did", async () => {
    const res = await initialize();
    expect(res.status).toBe(204);
    expect(await res.text()).toBe("");

    expect((await movePointer()).status).toBe(204);
  });

  it("refuses to re-seed an initialized instance and never discloses what it stores", async () => {
    await initialize();
    const res = await SELF.fetch(
      bearerReq(`/${SLUG}/initialize`, BOOTSTRAP, {
        method: "POST",
        body: JSON.stringify({ ownerToken: "owner-2", secret: "other" }),
      }),
    );
    expect(res.status).toBe(409);
    const body = await res.text();
    expect(body).not.toMatch(/owner-1/);
    expect(body).not.toMatch(new RegExp(SECRET));

    expect((await SELF.fetch(authedReq("/apps"))).status).toBe(200);
    expect((await SELF.fetch(bearerReq(`/${SLUG}/apps`, "other"))).status).toBe(401);
  });

  it("refuses to disclose the identity to the project secret", async () => {
    await initialize();
    const res = await SELF.fetch(
      bearerReq(`/${SLUG}/initialize`, SECRET, {
        method: "POST",
        body: JSON.stringify({ ownerToken: "owner-2", secret: "other" }),
      }),
    );
    expect(res.status).toBe(401);
    expect(await res.text()).not.toMatch(/owner-1/);
  });

  it("adopts the presented identity when force is set", async () => {
    await initialize();
    const res = await SELF.fetch(
      bearerReq(`/${SLUG}/initialize`, BOOTSTRAP, {
        method: "POST",
        body: JSON.stringify({ ownerToken: "owner-2", secret: "other", force: true }),
      }),
    );
    expect(res.status).toBe(204);
    expect(await res.text()).toBe("");
    expect((await SELF.fetch(bearerReq(`/${SLUG}/apps`, "other"))).status).toBe(200);
  });
});

describe("authenticated endpoints", () => {
  it("rejects a pointer move before the instance is initialized", async () => {
    expect((await movePointer()).status).toBe(401);
  });

  it("rejects a pointer move with no authorization header", async () => {
    await initialize();
    const res = await SELF.fetch(
      req(`/${SLUG}/move-pointer`, { method: "POST", body: pointerMoveBody() }),
    );
    expect(res.status).toBe(401);
  });

  it("rejects a pointer move with an incorrect project secret", async () => {
    await initialize();
    const res = await SELF.fetch(
      bearerReq(`/${SLUG}/move-pointer`, "wrong-secret", {
        method: "POST",
        body: pointerMoveBody(),
      }),
    );
    expect(res.status).toBe(401);
  });

  it("moves, then reports the promotion the pointer serves", async () => {
    await initialize();
    expect((await movePointer()).status).toBe(204);

    const served = await SELF.fetch(authedReq("/pointer"));
    expect(await served.json()).toEqual({ promotionId: "promo-1" });
    const preview = await SELF.fetch(authedReq("/pointer?pointer=pr-42"));
    expect(await preview.json()).toEqual({ promotionId: null });
  });

  it("refuses with 409 a pointer move that names a promotion the pointer no longer serves", async () => {
    await initialize();
    await movePointer();

    const res = await movePointer({ promotionId: "promo-2", replaces: "promo-0" });
    expect(res.status).toBe(409);

    const served = await SELF.fetch(authedReq("/pointer"));
    expect(await served.json()).toEqual({ promotionId: "promo-1" });
  });

  it("removes a pointer, leaving nothing served on it", async () => {
    await initialize();
    await movePointer({ pointer: "pr-42" });

    const res = await SELF.fetch(
      authedReq("/remove-pointer", { method: "POST", body: JSON.stringify({ pointer: "pr-42" }) }),
    );
    expect(res.status).toBe(204);

    const served = await SELF.fetch(authedReq("/pointer?pointer=pr-42"));
    expect(await served.json()).toEqual({ promotionId: null });
  });

  it("rejects a remove-pointer with no pointer (never wipes production implicitly)", async () => {
    await initialize();
    const res = await SELF.fetch(
      authedReq("/remove-pointer", { method: "POST", body: JSON.stringify({}) }),
    );
    expect(res.status).toBe(400);
  });

  it("names every app it has moved", async () => {
    await initialize();
    await movePointer({ records: [makeRecord(), makeRecord({ app: "admin" })] });

    const res = await SELF.fetch(authedReq("/apps"));
    expect(await res.json()).toEqual(["admin", "web"]);
  });

  it("reads and updates the root-stack version stamp", async () => {
    await initialize();
    const initial = await SELF.fetch(authedReq("/version-stamp"));
    expect(await initial.json()).toEqual({ version: null });

    const putRes = await SELF.fetch(
      authedReq("/version-stamp", { method: "PUT", body: JSON.stringify({ version: "v1" }) }),
    );
    expect(putRes.status).toBe(204);

    const after = await SELF.fetch(authedReq("/version-stamp"));
    expect(await after.json()).toEqual({ version: "v1" });
  });

  it("destroys the instance, freeing the slug", async () => {
    await initialize();
    await movePointer();

    const destroyRes = await SELF.fetch(authedReq("/destroy", { method: "POST" }));
    expect(destroyRes.status).toBe(204);

    expect((await movePointer()).status).toBe(401);
  });

  it("returns 400 on a malformed body", async () => {
    await initialize();
    const res = await SELF.fetch(authedReq("/move-pointer", { method: "POST", body: "not json" }));
    expect(res.status).toBe(400);
  });

  it("returns 400 on a pointer move that names no promotion", async () => {
    await initialize();
    const res = await SELF.fetch(
      authedReq("/move-pointer", {
        method: "POST",
        body: JSON.stringify({ records: [makeRecord()] }),
      }),
    );
    expect(res.status).toBe(400);
  });

  it("returns 404 for an unknown route", async () => {
    await initialize();
    const res = await SELF.fetch(authedReq("/nope"));
    expect(res.status).toBe(404);
  });

  it("returns 404 when no slug is given", async () => {
    const res = await SELF.fetch(bearerReq("/move-pointer", SECRET, { method: "POST" }));
    expect(res.status).toBe(404);
  });
});

describe("service-binding read path", () => {
  it("needs no secret to resolve the served record", async () => {
    const store = env.RELEASES_DO.get(env.RELEASES_DO.idFromName(SLUG));
    await store.movePointer({ promotionId: "promo-1", records: [makeRecord()] });

    const entry = new (await import("../src/index")).default(createExecutionContext(), env);
    expect(await entry.readPointerRecord({ slug: SLUG, app: "web" })).toEqual({
      kind: "record",
      release: "deploy-1",
      record: makeRecord(),
    });
    expect(
      await entry.readPointerRecord({ slug: SLUG, app: "web", knownRelease: "deploy-1" }),
    ).toEqual({
      kind: "unchanged",
      release: "deploy-1",
    });
  });

  it("routes a pointer move's pointer through to a named pointer", async () => {
    await initialize();
    const moveRes = await movePointer({
      promotionId: "prev-1",
      pointer: "flaky-web-2626",
      records: [makeRecord({ release: "preview-deploy" })],
      labels: [{ label: "flaky-web-aaaa", app: "web" }],
    });
    expect(moveRes.status).toBe(204);

    const entry = new (await import("../src/index")).default(createExecutionContext(), env);
    expect(await entry.readLabelRecord({ slug: SLUG, label: "flaky-web-aaaa" })).toEqual({
      kind: "record",
      release: "preview-deploy",
      record: makeRecord({ release: "preview-deploy" }),
    });
    expect(await entry.readPointerRecord({ slug: SLUG, app: "web" })).toEqual({
      kind: "no-pointer",
    });
  });

  it("resolves the app the pointer serves when the caller omits it", async () => {
    const store = env.RELEASES_DO.get(env.RELEASES_DO.idFromName(SLUG));
    await store.movePointer({ promotionId: "promo-1", records: [makeRecord()] });

    const entry = new (await import("../src/index")).default(createExecutionContext(), env);
    expect(await entry.readPointerRecord({ slug: SLUG })).toEqual({
      kind: "record",
      release: "deploy-1",
      record: makeRecord(),
    });
  });

  it("reports an ambiguous app when the pointer serves several", async () => {
    const store = env.RELEASES_DO.get(env.RELEASES_DO.idFromName(SLUG));
    await store.movePointer({
      promotionId: "promo-1",
      records: [makeRecord(), makeRecord({ app: "admin", release: "deploy-9" })],
    });

    const entry = new (await import("../src/index")).default(createExecutionContext(), env);
    expect(await entry.readPointerRecord({ slug: SLUG })).toEqual({
      kind: "ambiguous-app",
    });
  });

  it("resolves a preview label the pointer move carried to the record it serves", async () => {
    await initialize();
    const moveRes = await movePointer({
      promotionId: "prev-1",
      pointer: "pr-42",
      records: [makeRecord({ release: "preview-deploy" })],
      labels: [{ label: "pr-42-abcdefghijklmnopp3347l26", app: "web" }],
    });
    expect(moveRes.status).toBe(204);

    const entry = new (await import("../src/index")).default(createExecutionContext(), env);
    expect(
      await entry.readLabelRecord({ slug: SLUG, label: "pr-42-abcdefghijklmnopp3347l26" }),
    ).toEqual({
      kind: "record",
      release: "preview-deploy",
      record: makeRecord({ release: "preview-deploy" }),
    });
    expect(
      await entry.readLabelRecord({
        slug: SLUG,
        label: "pr-42-abcdefghijklmnopp3347l26",
        knownRelease: "preview-deploy",
      }),
    ).toEqual({ kind: "unchanged", release: "preview-deploy" });
    expect(await entry.readLabelRecord({ slug: SLUG, label: "pr-42-zzzz" })).toEqual({
      kind: "no-pointer",
    });
  });
});
