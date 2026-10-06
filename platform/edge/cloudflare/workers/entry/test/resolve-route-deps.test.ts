import { describe, expect, it } from "vitest";
import type { DeploymentRecord, DeploymentsBinding } from "../src/deployments";
import { type RouteDeps, resolveRouteDeps } from "../src/index";
import { answerEveryRecordWith } from "./origin-deps";

function makeRecord(over: Partial<DeploymentRecord> = {}): DeploymentRecord {
  return {
    app: "web",
    framework: "next",
    identity: "deploy-1",
    deploymentId: "deploy-1",
    buildId: "build-1",
    routingManifest: {
      buildId: "build-1",
      basePath: "",
      pathnames: [],
      routes: {},
      dispatch: {},
      i18n: { locales: ["en", "fr"], defaultLocale: "en" },
    },
    functionUrls: { "/": "https://fn.example.com" },
    assetPrefix: "build-1",
    isrPrefix: "prod/p1/web/build-1",
    createdAt: 1_000,
    ...over,
  };
}

function bindingReturning(
  identity: string | undefined,
  record: DeploymentRecord | undefined,
): DeploymentsBinding {
  return answerEveryRecordWith(async () => {
    if (!identity) return { kind: "no-pointer" };
    if (!record) return { kind: "dangling", identity };
    return { kind: "record", identity, record };
  });
}

function failingBinding(): DeploymentsBinding {
  return answerEveryRecordWith(async () => {
    throw new Error("store unreachable");
  });
}

const assetStore: RouteDeps["assetStore"] = {
  cache: { match: async () => undefined, put: async () => {} },
  waitUntil: () => {},
};

describe("resolveRouteDeps", () => {
  it("refuses a record whose edge bundle lies outside the deployment's own prefix", async () => {
    const record = makeRecord({
      edgeWorkers: {
        bundleKey: "prod/other/web/build-1/edge/bundle.json",
        id: "e1",
        compatDate: "2026-03-10",
      },
    });

    await expect(
      resolveRouteDeps(
        { binding: bindingReturning("deploy-1", record), slug: "p1", app: "web" },
        { assetStore },
      ),
    ).rejects.toThrow(/outside its own prefix/);
  });

  it("accepts a record whose edge bundle sits under the deployment's own prefix", async () => {
    const record = makeRecord({
      edgeWorkers: {
        bundleKey: "prod/p1/web/build-1/edge/bundle.json",
        id: "e1",
        compatDate: "2026-03-10",
      },
    });

    const deps = await resolveRouteDeps(
      { binding: bindingReturning("deploy-1", record), slug: "p1", app: "web" },
      { assetStore },
    );
    expect(deps).not.toBeInstanceOf(Response);
  });

  it("wires the resolved Deployment's manifest and functionUrls into RouteDeps", async () => {
    const record = makeRecord();
    const deps = await resolveRouteDeps(
      { binding: bindingReturning("deploy-1", record), app: "web" },
      { assetStore },
    );

    expect(deps).not.toBeInstanceOf(Response);
    const routeDeps = deps as RouteDeps;
    expect(routeDeps.manifest).toEqual(record.routingManifest);
    expect(routeDeps.functionUrls).toEqual(record.functionUrls);
    expect(routeDeps.deploymentId).toBe("deploy-1");
  });

  it("fills the asset store's prefix from the record's asset prefix", async () => {
    const record = makeRecord({ assetPrefix: "assets/p1/web/build-1" });
    const deps = await resolveRouteDeps(
      { binding: bindingReturning("deploy-1", record), app: "web" },
      { assetStore },
    );

    expect(deps).not.toBeInstanceOf(Response);
    expect((deps as RouteDeps).assetStore.assetPrefix).toBe("assets/p1/web/build-1");
  });

  it("fills the interception config's ISR prefix from the record's ISR prefix", async () => {
    const record = makeRecord({ isrPrefix: "prod/p1/web/build-1" });
    const store = { get: async () => null };
    const deps = await resolveRouteDeps(
      { binding: bindingReturning("deploy-1", record), app: "web" },
      { assetStore, interception: { store } },
    );

    expect(deps).not.toBeInstanceOf(Response);
    expect((deps as RouteDeps).interception?.config).toEqual({
      isrPrefix: "prod/p1/web/build-1",
    });
  });

  it("leaves interception undefined when no cache store is bound", async () => {
    const record = makeRecord();
    const deps = await resolveRouteDeps(
      { binding: bindingReturning("deploy-1", record), app: "web" },
      { assetStore },
    );

    expect((deps as RouteDeps).interception).toBeUndefined();
  });

  it("returns the baked-in 404 when the app has no active pointer", async () => {
    const deps = await resolveRouteDeps(
      { binding: bindingReturning(undefined, undefined), app: "web" },
      { assetStore },
    );

    expect(deps).toBeInstanceOf(Response);
    const response = deps as Response;
    expect(response.status).toBe(404);
    expect(await response.text()).toMatch(/deployment/i);
  });

  it("returns 501 for a Deployment that ships no routing manifest", async () => {
    const record = makeRecord({ framework: "node", routingManifest: undefined });
    const deps = await resolveRouteDeps(
      { binding: bindingReturning("deploy-1", record), app: "web" },
      { assetStore },
    );

    expect(deps).toBeInstanceOf(Response);
    const response = deps as Response;
    expect(response.status).toBe(501);
    expect(await response.text()).toMatch(/node/);
  });

  it("returns 503 when the store is unreachable on a cold isolate", async () => {
    const deps = await resolveRouteDeps({ binding: failingBinding(), app: "web" }, { assetStore });

    expect(deps).toBeInstanceOf(Response);
    expect((deps as Response).status).toBe(503);
  });
});

describe("the image origin of a routed deployment", () => {
  const entryUrl = "https://r1-x.o.example.com";

  function imageRecord(): DeploymentRecord {
    const record = makeRecord({ functionUrls: { "bundle-0": entryUrl } });
    return { ...record, routingManifest: { ...record.routingManifest!, entry: "bundle-0" } };
  }

  const payload = {
    assetPrefix: "build-1",
    url: "/a.png",
    w: 640,
    q: 75,
    accept: "image/webp",
    mimeType: "image/webp",
    configHash: "deadbeef",
  };

  function reached(fetched: string[]): typeof fetch {
    return (async (input: RequestInfo | URL) => {
      fetched.push(String(input));
      return new Response("bytes", { status: 200, headers: { "content-type": "image/webp" } });
    }) as typeof fetch;
  }

  async function depsWith(base: Partial<Parameters<typeof resolveRouteDeps>[1]>) {
    const deps = await resolveRouteDeps(
      { binding: bindingReturning("deploy-1", imageRecord()), app: "web" },
      { assetStore, ...base },
    );
    return deps as RouteDeps;
  }

  it("asks the deployment's own service for an image when no optimizer is bound", async () => {
    const fetched: string[] = [];
    const deps = await depsWith({ imagesAtDeployment: true, originFetch: reached(fetched) });

    const response = await deps.imageOrigin!(payload);

    expect(response.status).toBe(200);
    expect(fetched).toEqual(["https://r1-x.o.example.com/_ocel/image"]);
  });

  it("keeps the AWS optimizer when one is bound", async () => {
    const optimizer = async () => new Response("optimizer", { status: 200 });
    const deps = await depsWith({ imagesAtDeployment: false, imageOrigin: optimizer });

    expect(deps.imageOrigin).toBe(optimizer);
  });

  it("leaves an origin reached with AWS keys without a deployment image origin", async () => {
    const deps = await depsWith({ originFetch: reached([]) });

    expect(deps.imageOrigin).toBeUndefined();
  });

  it("answers unprovisioned when the deployment's service cannot be reached", async () => {
    const deps = await depsWith({
      imagesAtDeployment: true,
      originFetch: (async () => {
        throw new Error("unreachable");
      }) as typeof fetch,
    });

    const response = await deps.imageOrigin!(payload);

    expect(response.status).toBe(502);
    expect(await response.text()).toContain("No image optimizer is provisioned");
  });
});
