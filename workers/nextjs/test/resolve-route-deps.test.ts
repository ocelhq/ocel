import { describe, expect, it } from "vitest";

import { resolveRouteDeps, type RouteDeps } from "../src/index";
import type { DeploymentRecord, DeploymentsBinding } from "../src/deployments";

function makeRecord(over: Partial<DeploymentRecord> = {}): DeploymentRecord {
  return {
    app: "web",
    buildId: "build-1",
    routingManifest: {
      buildId: "build-1",
      basePath: "",
      pathnames: [],
      routes: {},
      dispatch: {},
    },
    functionUrls: { "/": "https://fn.example.com" },
    tagNamespace: "ns-1",
    assetPrefix: "build-1",
    createdAt: 1_000,
    ...over,
  };
}

function bindingReturning(
  buildId: string | undefined,
  record: DeploymentRecord | undefined,
): DeploymentsBinding {
  return {
    async activeBuildId() {
      return buildId;
    },
    async record() {
      return record;
    },
  };
}

function failingBinding(): DeploymentsBinding {
  return {
    async activeBuildId() {
      throw new Error("store unreachable");
    },
    async record() {
      throw new Error("store unreachable");
    },
  };
}

const assets: RouteDeps["assets"] = { fetch: async () => new Response("", { status: 404 }) };

describe("resolveRouteDeps", () => {
  it("wires the resolved Deployment's manifest and functionUrls into RouteDeps", async () => {
    const record = makeRecord();
    const deps = await resolveRouteDeps(
      { binding: bindingReturning("build-1", record), app: "web" },
      { assets },
    );

    expect(deps).not.toBeInstanceOf(Response);
    const routeDeps = deps as RouteDeps;
    expect(routeDeps.manifest).toEqual(record.routingManifest);
    expect(routeDeps.functionUrls).toEqual(record.functionUrls);
  });

  it("fills the interception config's prefix from the record's tag namespace", async () => {
    const record = makeRecord({ tagNamespace: "prod/p1/web/build-1" });
    const store = { get: async () => null };
    const deps = await resolveRouteDeps(
      { binding: bindingReturning("build-1", record), app: "web" },
      { assets, interception: { store } },
    );

    expect(deps).not.toBeInstanceOf(Response);
    expect((deps as RouteDeps).interception?.config).toEqual({
      prefix: "prod/p1/web/build-1",
    });
  });

  it("leaves interception undefined when no cache store is bound", async () => {
    const record = makeRecord();
    const deps = await resolveRouteDeps(
      { binding: bindingReturning("build-1", record), app: "web" },
      { assets },
    );

    expect((deps as RouteDeps).interception).toBeUndefined();
  });

  it("returns the baked-in 404 when the app has no active pointer", async () => {
    const deps = await resolveRouteDeps(
      { binding: bindingReturning(undefined, undefined), app: "web" },
      { assets },
    );

    expect(deps).toBeInstanceOf(Response);
    const response = deps as Response;
    expect(response.status).toBe(404);
    expect(await response.text()).toMatch(/deployment/i);
  });

  it("returns 503 when the store is unreachable on a cold isolate", async () => {
    const deps = await resolveRouteDeps(
      { binding: failingBinding(), app: "web" },
      { assets },
    );

    expect(deps).toBeInstanceOf(Response);
    expect((deps as Response).status).toBe(503);
  });
});
