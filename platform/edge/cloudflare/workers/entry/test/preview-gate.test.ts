import { createExecutionContext } from "cloudflare:test";
import gateEncodings from "@platform/edge-contract/fixtures/preview-gate" with { type: "json" };
import { hashPreviewPassword } from "@platform/edge-contract/preview-gate";
import { describe, expect, it } from "vitest";
import worker, { type Env } from "../src/index";
import { readPreviewGate } from "../src/preview-gate";
import type { ReleasesBinding } from "../src/releases";
import { answerEveryRecordWith, capturing, makeRecord, withGlobalFetch } from "./origin-deps";

const PREVIEW_KEY = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0";
const GATE_KEY = "gate-key-for-p1";
const PASSWORD = "correct horse";
const BYPASS = "bypass-secret-one";
const PROJECT_HOST = "pr-12-web-abcdefghijklmnopp3347l26.myapp.com";
const GLOBAL_HOST = "shop-abcdefghijklmnoproheemcq.preview.ocel.app";

const binding: ReleasesBinding = answerEveryRecordWith(async () => ({
  kind: "record",
  release: "deploy-1",
  record: makeRecord(),
}));

async function gateRecord(over: Record<string, unknown> = {}) {
  return {
    key: GATE_KEY,
    passwordMac: await hashPreviewPassword(GATE_KEY, PASSWORD),
    bypassSecrets: [BYPASS, "bypass-secret-two"],
    allowOptions: ["/api/"],
    ...over,
  };
}

async function projectEnv(over: Partial<Env> = {}): Promise<Env> {
  return {
    RELEASES: binding,
    OCEL_EDGE_ACCESS_KEY_ID: "AKIAEXAMPLE",
    OCEL_EDGE_SECRET_KEY: "secretkey",
    OCEL_SLUG: "p1",
    OCEL_PREVIEW: "1",
    OCEL_PREVIEW_BASE_DOMAIN: "myapp.com",
    OCEL_PREVIEW_KEY: PREVIEW_KEY,
    OCEL_PREVIEW_GATE: JSON.stringify(await gateRecord()),
    ...over,
  };
}

async function globalEnv(over: Partial<Env> = {}): Promise<Env> {
  return {
    RELEASES: binding,
    OCEL_EDGE_ACCESS_KEY_ID: "AKIAEXAMPLE",
    OCEL_EDGE_SECRET_KEY: "secretkey",
    OCEL_SLUG: "",
    OCEL_PREVIEW: "1",
    OCEL_PREVIEW_GLOBAL: "1",
    OCEL_PREVIEW_BASE_DOMAIN: "preview.ocel.app",
    OCEL_PREVIEW_KEY: PREVIEW_KEY,
    OCEL_PREVIEW_GATES: JSON.stringify({ shop: await gateRecord() }),
    ...over,
  };
}

function basic(password: string, user = "anyone"): string {
  return `Basic ${btoa(`${user}:${password}`)}`;
}

async function visit(request: Request, env: Env) {
  const wire = capturing();
  const response = await withGlobalFetch(wire.fetch, () =>
    worker.fetch(request, env, createExecutionContext()),
  );
  return { response, wire };
}

async function login(env: Env, host: string): Promise<string> {
  const { response } = await visit(
    new Request(`https://${host}/.ocel/preview/login`, {
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({ password: PASSWORD, next: "/users" }),
    }),
    env,
  );
  expect(response.status).toBe(303);
  return (response.headers.get("set-cookie") ?? "").split(";", 1)[0];
}

describe("the preview gate on a project's preview worker", () => {
  it("refuses a request without credentials, with a login form no cache may keep", async () => {
    const { response, wire } = await visit(
      new Request(`https://${PROJECT_HOST}/users`),
      await projectEnv(),
    );

    expect(response.status).toBe(401);
    expect(response.headers.get("cache-control")).toBe("private, no-store");
    expect(response.headers.get("x-robots-tag")).toBe("noindex");
    expect(await response.text()).toContain("<form");
    expect(wire.calls).toHaveLength(0);
  });

  it("serves the page to a request carrying the session cookie a login returned", async () => {
    const env = await projectEnv();
    const cookie = await login(env, PROJECT_HOST);

    const { response, wire } = await visit(
      new Request(`https://${PROJECT_HOST}/users`, { headers: { cookie } }),
      env,
    );

    expect(response.status).toBe(200);
    expect(await response.text()).toBe("origin");
    expect(wire.calls[0].headers.get("cookie") ?? "").not.toContain("__Host-ocel-preview");
  });

  it("refuses a session cookie presented on another preview host", async () => {
    const env = await projectEnv();
    const cookie = await login(env, PROJECT_HOST);

    const { response } = await visit(
      new Request("https://pr-13-web-abcdefghijklmnopp3347l26.myapp.com/users", {
        headers: { cookie },
      }),
      env,
    );

    expect(response.status).not.toBe(200);
  });

  it("serves the page to matching Basic credentials and strips them before the origin", async () => {
    const { response, wire } = await visit(
      new Request(`https://${PROJECT_HOST}/users`, {
        headers: { authorization: basic(PASSWORD) },
      }),
      await projectEnv(),
    );

    expect(response.status).toBe(200);
    expect(response.headers.get("x-robots-tag")).toBe("noindex");
    expect(wire.calls[0].headers.get("authorization") ?? "").not.toContain("Basic");
  });

  it("refuses wrong Basic credentials and a Bearer token", async () => {
    const env = await projectEnv();
    for (const authorization of [basic("wrong"), "Bearer abc"]) {
      const { response, wire } = await visit(
        new Request(`https://${PROJECT_HOST}/users`, { headers: { authorization } }),
        env,
      );
      expect(response.status).toBe(401);
      expect(wire.calls).toHaveLength(0);
    }
  });

  it("admits a request that carries a Bearer token beside a bypass secret", async () => {
    const { response, wire } = await visit(
      new Request(`https://${PROJECT_HOST}/users`, {
        headers: { authorization: "Bearer abc", "x-ocel-preview-bypass": BYPASS },
      }),
      await projectEnv(),
    );

    expect(response.status).toBe(200);
    expect(wire.calls).toHaveLength(1);
  });

  it("serves the page to any of the bypass secrets and hides the header from the origin", async () => {
    const env = await projectEnv();
    for (const secret of [BYPASS, "bypass-secret-two"]) {
      const { response, wire } = await visit(
        new Request(`https://${PROJECT_HOST}/users`, {
          headers: { "x-ocel-preview-bypass": secret },
        }),
        env,
      );
      expect(response.status).toBe(200);
      expect(wire.calls[0].headers.get("x-ocel-preview-bypass")).toBeNull();
    }
  });

  it("answers a bypass request that asks for a cookie with a redirect carrying it", async () => {
    const { response, wire } = await visit(
      new Request(`https://${PROJECT_HOST}/users`, {
        headers: { "x-ocel-preview-bypass": BYPASS, "x-ocel-set-bypass-cookie": "true" },
      }),
      await projectEnv(),
    );

    expect(response.status).toBe(303);
    expect(response.headers.get("set-cookie")).toContain("__Host-ocel-preview=");
    expect(wire.calls).toHaveLength(0);
  });

  it("refuses a request without credentials after one with credentials was served", async () => {
    const env = await projectEnv();
    const admitted = await visit(
      new Request(`https://${PROJECT_HOST}/users`, {
        headers: { "x-ocel-preview-bypass": BYPASS },
      }),
      env,
    );
    const refused = await visit(new Request(`https://${PROJECT_HOST}/users`), env);

    expect(admitted.response.status).toBe(200);
    expect(refused.response.status).toBe(401);
    expect(refused.wire.calls).toHaveLength(0);
  });

  it("answers an allowed OPTIONS path without credentials and refuses another", async () => {
    const env = await projectEnv();
    const allowed = await visit(
      new Request(`https://${PROJECT_HOST}/api/users`, { method: "OPTIONS" }),
      env,
    );
    const refused = await visit(
      new Request(`https://${PROJECT_HOST}/users`, { method: "OPTIONS" }),
      env,
    );

    expect(allowed.response.status).toBe(200);
    expect(refused.response.status).toBe(401);
  });

  it("refuses every request when the gate secret cannot be read", async () => {
    const { response, wire } = await visit(
      new Request(`https://${PROJECT_HOST}/users`, {
        headers: { "x-ocel-preview-bypass": BYPASS },
      }),
      await projectEnv({ OCEL_PREVIEW_GATE: "{not json" }),
    );

    expect(response.status).toBe(503);
    expect(wire.calls).toHaveLength(0);
  });

  it("serves a preview with no gate configured", async () => {
    const { response } = await visit(
      new Request(`https://${PROJECT_HOST}/users`),
      await projectEnv({ OCEL_PREVIEW_GATE: undefined }),
    );

    expect(response.status).toBe(200);
  });

  it("answers a preview host it cannot resolve with a 404 no search engine indexes", async () => {
    const { response, wire } = await visit(
      new Request("https://pr-12-web-abcdefghijklmnopaaaaaaaa.myapp.com/users"),
      await projectEnv(),
    );

    expect(response.status).toBe(404);
    expect(response.headers.get("x-robots-tag")).toBe("noindex");
    expect(wire.calls).toHaveLength(0);
  });

  it("does not gate a production host", async () => {
    const { response } = await visit(
      new Request("https://app.example.com/users"),
      await projectEnv({ OCEL_PREVIEW: undefined }),
    );

    expect(response.status).toBe(200);
  });
});

describe("the preview gate on the shared preview entry", () => {
  it("gates a project's preview with the keys recorded under its slug", async () => {
    const env = await globalEnv();
    const refused = await visit(new Request(`https://${GLOBAL_HOST}/users`), env);
    const admitted = await visit(
      new Request(`https://${GLOBAL_HOST}/users`, {
        headers: { authorization: basic(PASSWORD) },
      }),
      env,
    );

    expect(refused.response.status).toBe(401);
    expect(refused.wire.calls).toHaveLength(0);
    expect(admitted.response.status).toBe(200);
  });

  it("does not admit another key's password to a project's preview", async () => {
    const other = await gateRecord({
      key: "other-key",
      passwordMac: await hashPreviewPassword("other-key", "x"),
    });
    const env = await globalEnv({ OCEL_PREVIEW_GATES: JSON.stringify({ shop: other }) });

    const { response } = await visit(
      new Request(`https://${GLOBAL_HOST}/users`, { headers: { authorization: basic(PASSWORD) } }),
      env,
    );

    expect(response.status).toBe(401);
  });

  it("serves a project that has no recorded keys", async () => {
    const { response } = await visit(
      new Request(`https://${GLOBAL_HOST}/users`),
      await globalEnv({ OCEL_PREVIEW_GATES: JSON.stringify({ other: {} }) }),
    );

    expect(response.status).toBe(200);
  });

  it("refuses every request when the recorded keys cannot be read", async () => {
    const { response } = await visit(
      new Request(`https://${GLOBAL_HOST}/users`),
      await globalEnv({ OCEL_PREVIEW_GATES: "[]" }),
    );

    expect(response.status).toBe(503);
  });
});

describe("the gate keys as the Go side encodes them", () => {
  for (const example of gateEncodings.gates) {
    it(`reads ${example.name} from a project's worker secret`, () => {
      expect(readPreviewGate({ OCEL_PREVIEW_GATE: example.encoded }, "p1")).toEqual({
        kind: "gated",
        gate: example.gate,
      });
    });
  }

  it("reads a project's keys from the shared entry's worker secret", () => {
    const { slug, encoded } = gateEncodings.sharedEntry;
    expect(readPreviewGate({ OCEL_PREVIEW_GATES: encoded }, slug)).toEqual({
      kind: "gated",
      gate: gateEncodings.gates[0]?.gate,
    });
  });

  it("refuses a request without credentials rather than answering 503", async () => {
    const { response } = await visit(
      new Request(`https://${PROJECT_HOST}/users`),
      await projectEnv({ OCEL_PREVIEW_GATE: gateEncodings.gates[0]?.encoded }),
    );

    expect(response.status).toBe(401);
  });
});
