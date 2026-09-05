import { createExecutionContext } from "cloudflare:test";
import { describe, expect, it } from "vitest";

import worker, { type Env } from "../src/index";
import type {
  DeploymentRecord,
  DeploymentsBinding,
  PointerRecordResult,
} from "../src/deployments";
import { FN_URL, capturing, makeRecord, withGlobalFetch } from "./origin-deps";

type PointerArgs = Parameters<DeploymentsBinding["pointerRecord"]>[0];

function answering(
  result: PointerRecordResult,
): DeploymentsBinding & { calls: PointerArgs[] } {
  return {
    calls: [],
    async pointerRecord(args: PointerArgs) {
      this.calls.push(args);
      return result;
    },
  };
}

function recording(
  record: DeploymentRecord,
): DeploymentsBinding & { calls: PointerArgs[] } {
  return answering({ kind: "record", identity: record.identity, record });
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
    const binding = recording(makeRecord());
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
    expect(binding.calls[0].pointer).toBeUndefined();
    expect(response.status).toBe(200);
    expect(await response.text()).toBe("origin");
    expect(wire.calls[0].url).toBe(FN_URL + "users");
  });

  it("answers the baked-in 404 when the slug carries more than one app", async () => {
    const binding = answering({ kind: "ambiguous-app" });
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
    const binding = recording(makeRecord());

    const response = await worker.fetch(
      new Request("https://app.example.com/users"),
      makeEnv(binding, { OCEL_SLUG: undefined as unknown as string }),
      createExecutionContext(),
    );

    expect(response.status).toBe(404);
    expect(binding.calls).toHaveLength(0);
  });
});

describe("preview host routing", () => {
  it("names the app the preview host carries", async () => {
    const binding = recording(makeRecord());
    const wire = capturing();

    await withGlobalFetch(wire.fetch, () =>
      worker.fetch(
        new Request("https://pr-42.myapp.com/users"),
        makeEnv(binding, {
          OCEL_PREVIEW: "1",
          OCEL_PREVIEW_BASE_DOMAIN: "myapp.com",
          OCEL_PREVIEW_APPS: "api",
        }),
        createExecutionContext(),
      ),
    );

    expect(binding.calls[0].app).toBe("api");
    expect(binding.calls[0].pointer).toBe("pr-42");
  });

  it("answers the baked-in 404 for a preview host off the base domain", async () => {
    const binding = recording(makeRecord());

    const response = await worker.fetch(
      new Request("https://elsewhere.example.com/users"),
      makeEnv(binding, {
        OCEL_PREVIEW: "1",
        OCEL_PREVIEW_BASE_DOMAIN: "myapp.com",
        OCEL_PREVIEW_APPS: "api",
      }),
      createExecutionContext(),
    );

    expect(response.status).toBe(404);
    expect(binding.calls).toHaveLength(0);
  });
});
