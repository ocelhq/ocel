import { SELF, createExecutionContext, env } from "cloudflare:test";
import { describe, expect, it } from "vitest";

import { SCHEMA_VERSION } from "../src/store";
import type { DeploymentRecord } from "../src/store";
import type { Env } from "../src/env";

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

function makeRecord(over: Partial<DeploymentRecord> = {}): DeploymentRecord {
  return {
    app: "web",
    runtime: "next",
    identity: "deploy-1",
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

describe("schema version", () => {
  it("reports the schema the store speaks, without a credential", async () => {
    const res = await SELF.fetch(req(`/${SLUG}/schema-version`));
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({ schemaVersion: SCHEMA_VERSION });
  });
});

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

  it("seeds the instance and reports the identity it now carries", async () => {
    const res = await initialize();
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({ ownerToken: "owner-1", secret: SECRET });

    const staged = await SELF.fetch(
      authedReq("/staged", { method: "PUT", body: JSON.stringify(makeRecord()) }),
    );
    expect(staged.status).toBe(204);
  });

  it("returns the existing identity for an already-initialized instance", async () => {
    await initialize();
    const res = await SELF.fetch(
      bearerReq(`/${SLUG}/initialize`, BOOTSTRAP, {
        method: "POST",
        body: JSON.stringify({ ownerToken: "owner-2", secret: "other" }),
      }),
    );
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({ ownerToken: "owner-1", secret: SECRET });

    expect(
      (await SELF.fetch(authedReq("/staged", { method: "PUT", body: JSON.stringify(makeRecord()) })))
        .status,
    ).toBe(204);
    expect(
      (
        await SELF.fetch(
          bearerReq(`/${SLUG}/staged`, "other", {
            method: "PUT",
            body: JSON.stringify(makeRecord()),
          }),
        )
      ).status,
    ).toBe(401);
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
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({ ownerToken: "owner-2", secret: "other" });
  });
});

describe("authenticated write endpoint", () => {
  it("rejects a write before the instance is initialized", async () => {
    const res = await SELF.fetch(
      authedReq("/staged", { method: "PUT", body: JSON.stringify(makeRecord()) }),
    );
    expect(res.status).toBe(401);
  });

  it("rejects a write with no authorization header", async () => {
    await initialize();
    const res = await SELF.fetch(
      req(`/${SLUG}/staged`, { method: "PUT", body: JSON.stringify(makeRecord()) }),
    );
    expect(res.status).toBe(401);
  });

  it("rejects a write with an incorrect project secret", async () => {
    await initialize();
    const res = await SELF.fetch(
      bearerReq(`/${SLUG}/staged`, "wrong-secret", {
        method: "PUT",
        body: JSON.stringify(makeRecord()),
      }),
    );
    expect(res.status).toBe(401);
  });

  it("accepts a correctly-signed putStaged and stores the record", async () => {
    await initialize();
    const putRes = await SELF.fetch(
      authedReq("/staged", { method: "PUT", body: JSON.stringify(makeRecord()) }),
    );
    expect(putRes.status).toBe(204);

    const store = env.DEPLOYMENTS_DO.get(env.DEPLOYMENTS_DO.idFromName(SLUG));
    expect(await store.record("web", "deploy-1")).toEqual(makeRecord());
  });

  it("promotes, then reports it through history", async () => {
    await initialize();
    await SELF.fetch(
      authedReq("/staged", { method: "PUT", body: JSON.stringify(makeRecord()) }),
    );
    const promoteRes = await SELF.fetch(
      authedReq("/promote", {
        method: "POST",
        body: JSON.stringify({ promotionId: "promo-1", ts: 1_000, builds: { web: "deploy-1" } }),
      }),
    );
    expect(promoteRes.status).toBe(204);

    const historyRes = await SELF.fetch(authedReq("/history"));
    expect(await historyRes.json()).toEqual([
      { promotionId: "promo-1", ts: 1_000, builds: { web: "deploy-1" }, active: true },
    ]);
  });

  it("rejects a promote whose tag is already in use with 409", async () => {
    await initialize();
    await SELF.fetch(
      authedReq("/promote", {
        method: "POST",
        body: JSON.stringify({ promotionId: "promo-1", ts: 1_000, builds: { web: "b1" }, tag: "v1.2.3" }),
      }),
    );

    const clashRes = await SELF.fetch(
      authedReq("/promote", {
        method: "POST",
        body: JSON.stringify({ promotionId: "promo-2", ts: 2_000, builds: { web: "b2" }, tag: "v1.2.3" }),
      }),
    );

    expect(clashRes.status).toBe(409);
    expect(await clashRes.text()).toMatch(/already used by promotion promo-1/);
  });

  it("prunes and reports what was removed", async () => {
    await initialize();
    for (const identity of ["deploy-1", "deploy-2", "deploy-3"]) {
      await SELF.fetch(
        authedReq("/staged", {
          method: "PUT",
          body: JSON.stringify(makeRecord({ identity })),
        }),
      );
      await SELF.fetch(
        authedReq("/promote", {
          method: "POST",
          body: JSON.stringify({
            promotionId: `promo-${identity}`,
            ts: 1_000,
            builds: { web: identity },
          }),
        }),
      );
    }

    const pruneRes = await SELF.fetch(
      authedReq("/prune", { method: "POST", body: JSON.stringify({ keepN: 1 }) }),
    );
    expect(pruneRes.status).toBe(200);
    const result = (await pruneRes.json()) as { removedPromotionIds: string[] };
    expect(result.removedPromotionIds).toEqual(["promo-deploy-2", "promo-deploy-1"]);
  });

  it("removes a whole pointer and reports what was reclaimed", async () => {
    await initialize();
    await SELF.fetch(
      authedReq("/staged", {
        method: "PUT",
        body: JSON.stringify(makeRecord({ identity: "pr-1" })),
      }),
    );
    await SELF.fetch(
      authedReq("/promote", {
        method: "POST",
        body: JSON.stringify({ promotionId: "promo-pr-1", ts: 1_000, builds: { web: "pr-1" }, pointer: "pr-42" }),
      }),
    );

    const res = await SELF.fetch(
      authedReq("/remove-pointer", { method: "POST", body: JSON.stringify({ pointer: "pr-42" }) }),
    );
    expect(res.status).toBe(200);
    const result = (await res.json()) as {
      removedPromotionIds: string[];
      removedRecordKeys: string[];
    };
    expect(result.removedPromotionIds).toEqual(["promo-pr-1"]);
    expect(result.removedRecordKeys).toEqual(["record:web/pr-1"]);

    const history = await (await SELF.fetch(authedReq("/history?pointer=pr-42"))).json();
    expect(history).toEqual([]);
  });

  it("rejects a remove-pointer with no pointer (never wipes production implicitly)", async () => {
    await initialize();
    const res = await SELF.fetch(
      authedReq("/remove-pointer", { method: "POST", body: JSON.stringify({}) }),
    );
    expect(res.status).toBe(400);
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
    await SELF.fetch(
      authedReq("/staged", { method: "PUT", body: JSON.stringify(makeRecord()) }),
    );

    const destroyRes = await SELF.fetch(authedReq("/destroy", { method: "POST" }));
    expect(destroyRes.status).toBe(204);

    const after = await SELF.fetch(
      authedReq("/staged", { method: "PUT", body: JSON.stringify(makeRecord()) }),
    );
    expect(after.status).toBe(401);
  });

  it("returns 400 on a malformed body", async () => {
    await initialize();
    const res = await SELF.fetch(
      authedReq("/promote", { method: "POST", body: "not json" }),
    );
    expect(res.status).toBe(400);
  });

  it("returns 404 for an unknown route", async () => {
    await initialize();
    const res = await SELF.fetch(authedReq("/nope"));
    expect(res.status).toBe(404);
  });

  it("returns 404 when no slug is given", async () => {
    const res = await SELF.fetch(bearerReq("/staged", SECRET, { method: "PUT" }));
    expect(res.status).toBe(404);
  });
});

describe("service-binding read path", () => {
  it("needs no secret to resolve the active record", async () => {
    const store = env.DEPLOYMENTS_DO.get(env.DEPLOYMENTS_DO.idFromName(SLUG));
    await store.putStaged(makeRecord());
    await store.promote({ promotionId: "promo-1", ts: 1_000, builds: { web: "deploy-1" } });

    const entry = new (await import("../src/index")).default(
      createExecutionContext(),
      env,
    );
    expect(await entry.pointerRecord({ slug: SLUG, app: "web" })).toEqual({
      kind: "record",
      identity: "deploy-1",
      record: makeRecord(),
    });
    expect(
      await entry.pointerRecord({ slug: SLUG, app: "web", knownIdentity: "deploy-1" }),
    ).toEqual({
      kind: "unchanged",
      identity: "deploy-1",
    });
  });

  it("routes a promote's pointer through to a named pointer", async () => {
    await initialize();
    await SELF.fetch(
      authedReq("/staged", {
        method: "PUT",
        body: JSON.stringify(makeRecord({ identity: "preview-deploy" })),
      }),
    );
    const promoteRes = await SELF.fetch(
      authedReq("/promote", {
        method: "POST",
        body: JSON.stringify({
          promotionId: "prev-1",
          ts: 1_000,
          builds: { web: "preview-deploy" },
          pointer: "flaky-web-2626",
        }),
      }),
    );
    expect(promoteRes.status).toBe(204);

    const entry = new (await import("../src/index")).default(
      createExecutionContext(),
      env,
    );
    expect(
      await entry.pointerRecord({ slug: SLUG, app: "web", pointer: "flaky-web-2626" }),
    ).toEqual({
      kind: "record",
      identity: "preview-deploy",
      record: makeRecord({ identity: "preview-deploy" }),
    });
    expect(await entry.pointerRecord({ slug: SLUG, app: "web" })).toEqual({
      kind: "no-pointer",
    });
  });

  it("resolves the app from the promotion when the caller omits it", async () => {
    const store = env.DEPLOYMENTS_DO.get(env.DEPLOYMENTS_DO.idFromName(SLUG));
    await store.putStaged(makeRecord());
    await store.promote({ promotionId: "promo-1", ts: 1_000, builds: { web: "deploy-1" } });

    const entry = new (await import("../src/index")).default(
      createExecutionContext(),
      env,
    );
    expect(await entry.pointerRecord({ slug: SLUG })).toEqual({
      kind: "record",
      identity: "deploy-1",
      record: makeRecord(),
    });
  });

  it("reports an ambiguous app when the promotion carries several", async () => {
    const store = env.DEPLOYMENTS_DO.get(env.DEPLOYMENTS_DO.idFromName(SLUG));
    await store.putStaged(makeRecord());
    await store.putStaged(makeRecord({ app: "admin", identity: "deploy-9" }));
    await store.promote({
      promotionId: "promo-1",
      ts: 1_000,
      builds: { web: "deploy-1", admin: "deploy-9" },
    });

    const entry = new (await import("../src/index")).default(
      createExecutionContext(),
      env,
    );
    expect(await entry.pointerRecord({ slug: SLUG })).toEqual({
      kind: "ambiguous-app",
    });
  });
});
