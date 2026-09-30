import { createExecutionContext } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import type { DeploymentRecord, DeploymentsBinding, PointerRecordResult } from "../src/deployments";
import worker, { type Env } from "../src/index";
import { capturing, FN_URL, makeRecord, withGlobalFetch } from "./origin-deps";

type PointerArgs = Parameters<DeploymentsBinding["readPointerRecord"]>[0];
type LabelArgs = Parameters<DeploymentsBinding["readLabelRecord"]>[0];
type Recording = DeploymentsBinding & { calls: PointerArgs[]; labels: LabelArgs[] };

function createRecordingBinding(result: PointerRecordResult): Recording {
  const binding: Recording = {
    calls: [],
    labels: [],
    async readPointerRecord(args) {
      binding.calls.push(args);
      return result;
    },
    async readLabelRecord(args) {
      binding.labels.push(args);
      return result;
    },
  };
  return binding;
}

function createRecordBinding(record: DeploymentRecord): Recording {
  return createRecordingBinding({ kind: "record", identity: record.identity, record });
}

function makeEnv(binding: DeploymentsBinding, over: Partial<Env> = {}): Env {
  return {
    DEPLOYMENTS: binding,
    OCEL_SLUG: "p1",
    OCEL_EDGE_ACCESS_KEY_ID: "AKIAEXAMPLE",
    OCEL_EDGE_SECRET_KEY: "secretkey",
    ...over,
  };
}

describe("production resolution of the slug's sole app", () => {
  it("leaves the app unnamed so the store resolves the project's sole one", async () => {
    const binding = createRecordBinding(makeRecord());
    const wire = capturing();

    const response = await withGlobalFetch(wire.fetch, () =>
      worker.fetch(
        new Request("https://app.example.com/users"),
        makeEnv(binding),
        createExecutionContext(),
      ),
    );

    expect(binding.calls).toHaveLength(1);
    expect(binding.calls[0].slug).toBe("p1");
    expect(binding.calls[0].app).toBeUndefined();
    expect(binding.labels).toHaveLength(0);
    expect(response.status).toBe(200);
    expect(await response.text()).toBe("origin");
    expect(wire.calls[0].url).toBe(`${FN_URL}users`);
  });

  it("answers the baked-in 404 when the slug has more than one app", async () => {
    const binding = createRecordingBinding({ kind: "ambiguous-app" });
    const wire = capturing();

    const response = await withGlobalFetch(wire.fetch, () =>
      worker.fetch(
        new Request("https://app.example.com/users"),
        makeEnv(binding),
        createExecutionContext(),
      ),
    );

    expect(response.status).toBe(404);
    expect(await response.text()).toContain("No deployment yet");
    expect(wire.calls).toHaveLength(0);
  });

  it("answers the baked-in 404 without asking the store when no slug is baked in", async () => {
    const binding = createRecordBinding(makeRecord());

    const response = await worker.fetch(
      new Request("https://app.example.com/users"),
      makeEnv(binding, { OCEL_SLUG: undefined as unknown as string }),
      createExecutionContext(),
    );

    expect(response.status).toBe(404);
    expect(binding.calls).toHaveLength(0);
  });
});

const PREVIEW_KEY = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0";

describe("preview host routing", () => {
  it("resolves a project preview host by its full label under the baked-in slug", async () => {
    const binding = createRecordBinding(makeRecord());
    const wire = capturing();

    const response = await withGlobalFetch(wire.fetch, () =>
      worker.fetch(
        new Request("https://pr-12-web-abcdefghijklmnopp3347l26.myapp.com/users"),
        makeEnv(binding, {
          OCEL_PREVIEW: "1",
          OCEL_PREVIEW_BASE_DOMAIN: "myapp.com",
          OCEL_PREVIEW_KEY: PREVIEW_KEY,
        }),
        createExecutionContext(),
      ),
    );

    expect(response.status).toBe(200);
    expect(binding.calls).toHaveLength(0);
    expect(binding.labels).toEqual([
      { slug: "p1", label: "pr-12-web-abcdefghijklmnopp3347l26", knownIdentity: undefined },
    ]);
  });

  it("resolves a global preview host under the slug its label names", async () => {
    const binding = createRecordBinding(makeRecord());
    const wire = capturing();

    await withGlobalFetch(wire.fetch, () =>
      worker.fetch(
        new Request("https://shop-abcdefghijklmnoproheemcq.preview.ocel.app/users"),
        makeEnv(binding, {
          OCEL_SLUG: "",
          OCEL_PREVIEW: "1",
          OCEL_PREVIEW_GLOBAL: "1",
          OCEL_PREVIEW_BASE_DOMAIN: "preview.ocel.app",
          OCEL_PREVIEW_KEY: PREVIEW_KEY,
        }),
        createExecutionContext(),
      ),
    );

    expect(binding.labels[0]).toMatchObject({
      slug: "shop",
      label: "shop-abcdefghijklmnoproheemcq",
    });
  });

  it("answers the baked-in 404 for a forged label without asking the store", async () => {
    const binding = createRecordBinding(makeRecord());

    const response = await worker.fetch(
      new Request("https://shop-abcdefghijklmnopaaaaaaaa.preview.ocel.app/users"),
      makeEnv(binding, {
        OCEL_SLUG: "",
        OCEL_PREVIEW: "1",
        OCEL_PREVIEW_GLOBAL: "1",
        OCEL_PREVIEW_BASE_DOMAIN: "preview.ocel.app",
        OCEL_PREVIEW_KEY: PREVIEW_KEY,
      }),
      createExecutionContext(),
    );

    expect(response.status).toBe(404);
    expect(binding.calls).toHaveLength(0);
    expect(binding.labels).toHaveLength(0);
  });

  it("answers the baked-in 404 for a preview host off the base domain", async () => {
    const binding = createRecordBinding(makeRecord());

    const response = await worker.fetch(
      new Request("https://elsewhere.example.com/users"),
      makeEnv(binding, {
        OCEL_PREVIEW: "1",
        OCEL_PREVIEW_BASE_DOMAIN: "myapp.com",
        OCEL_PREVIEW_KEY: PREVIEW_KEY,
      }),
      createExecutionContext(),
    );

    expect(response.status).toBe(404);
    expect(binding.calls).toHaveLength(0);
    expect(binding.labels).toHaveLength(0);
  });
});
