import { SELF, createExecutionContext, env } from "cloudflare:test";
import { describe, expect, it } from "vitest";

import type { DeploymentRecord } from "../src/store";
import type { Env } from "../src/env";

declare module "cloudflare:test" {
  interface ProvidedEnv extends Env {}
}

const SECRET = "dev-secret"; // matches wrangler.jsonc's vars.WRITE_SECRET

function req(path: string, init: RequestInit = {}) {
  return new Request(`https://store.example${path}`, init);
}

function authedReq(path: string, init: RequestInit = {}) {
  return req(path, {
    ...init,
    headers: { ...init.headers, authorization: `Bearer ${SECRET}` },
  });
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

describe("authenticated write endpoint", () => {
  it("rejects a write with no authorization header", async () => {
    const res = await SELF.fetch(
      req("/staged", { method: "PUT", body: JSON.stringify(makeRecord()) }),
    );
    expect(res.status).toBe(401);
  });

  it("rejects a write with an incorrect secret", async () => {
    const res = await SELF.fetch(
      req("/staged", {
        method: "PUT",
        headers: { authorization: "Bearer wrong-secret" },
        body: JSON.stringify(makeRecord()),
      }),
    );
    expect(res.status).toBe(401);
  });

  it("accepts a correctly-signed putStaged and stores the record", async () => {
    const putRes = await SELF.fetch(
      authedReq("/staged", { method: "PUT", body: JSON.stringify(makeRecord()) }),
    );
    expect(putRes.status).toBe(204);

    const store = env.DEPLOYMENTS_DO.get(env.DEPLOYMENTS_DO.idFromName("root"));
    expect(await store.record("web", "build-1")).toEqual(makeRecord());
  });

  it("promotes, then reports it through history", async () => {
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

  it("prunes and reports what was removed", async () => {
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
    const initial = await SELF.fetch(authedReq("/version-stamp"));
    expect(await initial.json()).toEqual({ version: null });

    const putRes = await SELF.fetch(
      authedReq("/version-stamp", { method: "PUT", body: JSON.stringify({ version: "v1" }) }),
    );
    expect(putRes.status).toBe(204);

    const after = await SELF.fetch(authedReq("/version-stamp"));
    expect(await after.json()).toEqual({ version: "v1" });
  });

  it("returns 400 on a malformed body", async () => {
    const res = await SELF.fetch(
      authedReq("/promote", { method: "POST", body: "not json" }),
    );
    expect(res.status).toBe(400);
  });

  it("returns 404 for an unknown route", async () => {
    const res = await SELF.fetch(authedReq("/nope"));
    expect(res.status).toBe(404);
  });
});

describe("service-binding read path", () => {
  it("needs no secret to resolve the active build id and record", async () => {
    const store = env.DEPLOYMENTS_DO.get(env.DEPLOYMENTS_DO.idFromName("root"));
    await store.putStaged(makeRecord());
    await store.promote({ promotionId: "promo-1", ts: 1_000, builds: { web: "build-1" } });

    // Exercises the same entrypoint a service binding would call — no
    // Authorization header at all. createExecutionContext gives the
    // WorkerEntrypoint a real ExecutionContext, the same one the runtime
    // would construct it with.
    const entry = new (await import("../src/index")).default(
      createExecutionContext(),
      env,
    );
    expect(await entry.activeBuildId("web")).toBe("build-1");
    expect(await entry.record("web", "build-1")).toEqual(makeRecord());
  });
});
