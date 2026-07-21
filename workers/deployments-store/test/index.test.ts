import { SELF, createExecutionContext, env } from "cloudflare:test";
import { describe, expect, it } from "vitest";

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

// Seeds the project's instance with SECRET (authorized by the bootstrap
// credential), the precondition for every per-project op.
async function initialize(slug = SLUG, secret = SECRET) {
  return SELF.fetch(
    bearerReq(`/${slug}/initialize`, BOOTSTRAP, {
      method: "POST",
      body: JSON.stringify({ ownerToken: "owner-1", secret }),
    }),
  );
}

// A per-project request authenticated with the seeded project secret.
function authedReq(path: string, init: RequestInit = {}) {
  return bearerReq(`/${SLUG}${path}`, SECRET, init);
}

function makeRecord(over: Partial<DeploymentRecord> = {}): DeploymentRecord {
  return {
    app: "web",
    buildId: "build-1",
    routingManifest: { pathnames: [] },
    functionUrls: { "/": "https://fn.example.com" },
    assetPrefix: "build-1",
    isrPrefix: "prod/p1/web/build-1",
    createdAt: 1_000,
    ...over,
  };
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

  it("seeds the instance so per-project ops authenticate against it", async () => {
    expect((await initialize()).status).toBe(204);

    const res = await SELF.fetch(
      authedReq("/staged", { method: "PUT", body: JSON.stringify(makeRecord()) }),
    );
    expect(res.status).toBe(204);
  });

  it("refuses a colliding owner token with 409", async () => {
    await initialize();
    const res = await SELF.fetch(
      bearerReq(`/${SLUG}/initialize`, BOOTSTRAP, {
        method: "POST",
        body: JSON.stringify({ ownerToken: "owner-2", secret: "other" }),
      }),
    );
    expect(res.status).toBe(409);
    expect(await res.text()).toMatch(/already owned by a different project/);
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
    expect(await store.record("web", "build-1")).toEqual(makeRecord());
  });

  it("promotes, then reports it through history", async () => {
    await initialize();
    await SELF.fetch(
      authedReq("/staged", { method: "PUT", body: JSON.stringify(makeRecord()) }),
    );
    const promoteRes = await SELF.fetch(
      authedReq("/promote", {
        method: "POST",
        body: JSON.stringify({ promotionId: "promo-1", ts: 1_000, builds: { web: "build-1" } }),
      }),
    );
    expect(promoteRes.status).toBe(204);

    const historyRes = await SELF.fetch(authedReq("/history"));
    expect(await historyRes.json()).toEqual([
      { promotionId: "promo-1", ts: 1_000, builds: { web: "build-1" }, active: true },
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
    for (const buildId of ["build-1", "build-2", "build-3"]) {
      await SELF.fetch(
        authedReq("/staged", {
          method: "PUT",
          body: JSON.stringify(makeRecord({ buildId })),
        }),
      );
      await SELF.fetch(
        authedReq("/promote", {
          method: "POST",
          body: JSON.stringify({
            promotionId: `promo-${buildId}`,
            ts: 1_000,
            builds: { web: buildId },
          }),
        }),
      );
    }

    const pruneRes = await SELF.fetch(
      authedReq("/prune", { method: "POST", body: JSON.stringify({ keepN: 1 }) }),
    );
    expect(pruneRes.status).toBe(200);
    const result = (await pruneRes.json()) as { removedPromotionIds: string[] };
    expect(result.removedPromotionIds).toEqual(["promo-build-2", "promo-build-1"]);
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

    // The secret is gone with the storage, so the old secret no longer authenticates.
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
  it("needs no secret to resolve the active build id and record", async () => {
    const store = env.DEPLOYMENTS_DO.get(env.DEPLOYMENTS_DO.idFromName(SLUG));
    await store.putStaged(makeRecord());
    await store.promote({ promotionId: "promo-1", ts: 1_000, builds: { web: "build-1" } });

    // Exercises the same entrypoint a service binding would call — no
    // Authorization header at all — carrying the project slug as the leading
    // RPC argument. createExecutionContext gives the WorkerEntrypoint a real
    // ExecutionContext, the same one the runtime would construct it with.
    const entry = new (await import("../src/index")).default(
      createExecutionContext(),
      env,
    );
    expect(await entry.activeBuildId(SLUG, "web")).toBe("build-1");
    expect(await entry.record(SLUG, "web", "build-1")).toEqual(makeRecord());
  });
});
