import { createExecutionContext } from "cloudflare:test";
import type { NextRouteTable } from "@framework/next-protocol/route-table";
import gateEncodings from "@platform/edge-contract/fixtures/gate" with { type: "json" };
import { hashPassword } from "@platform/edge-contract/gate";
import { describe, expect, it } from "vitest";
import vectors from "../../../../../../pkg/edge/edgeconformance/gate.json" with { type: "json" };
import { gateRequest, readGate } from "../src/gate";
import worker, { type Env } from "../src/index";
import type { ReleasesBinding } from "../src/releases";
import {
  answerEveryRecordWith,
  awsOrigin,
  capturing,
  FN_URL,
  makeRecord,
  storeOrigin,
  withGlobalFetch,
} from "./origin-deps";
import { routeTableKey } from "./route-table-store";
import { contextWithExports, serveAnswering } from "./worker-context";

const PREVIEW_KEY = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0";
const GATE_KEY = "gate-key-for-p1";
const PASSWORD = "correct horse";
const BYPASS = "bypass-secret-one";
const PROJECT_HOST = "pr-12-web-abcdefghijklmnopp3347l26.myapp.com";
const GLOBAL_HOST = "shop-abcdefghijklmnoproheemcq.preview.ocel.app";

const answer = async () => ({ kind: "record" as const, release: "deploy-1", record: makeRecord() });

function bindingHolding(gates: Record<string, string | Error>): ReleasesBinding {
  return {
    ...answerEveryRecordWith(answer),
    readGate: async ({ slug }) => {
      const held = gates[slug];
      if (held instanceof Error) throw held;
      return held;
    },
  };
}

let passwordHashed: Promise<string> | undefined;

async function gateRecord(over: Record<string, unknown> = {}) {
  passwordHashed ??= hashPassword(PASSWORD);
  return {
    key: GATE_KEY,
    passwordHash: await passwordHashed,
    bypassSecrets: [BYPASS, "bypass-secret-two"],
    allowOptions: ["/api/"],
    ...over,
  };
}

async function projectEnv(over: Partial<Env> = {}): Promise<Env> {
  return {
    RELEASES: bindingHolding({ p1: JSON.stringify(await gateRecord()) }),
    OCEL_ORIGIN: awsOrigin(),
    OCEL_SLUG: "p1",
    OCEL_PREVIEW: "1",
    OCEL_PREVIEW_BASE_DOMAIN: "myapp.com",
    OCEL_PREVIEW_KEY: PREVIEW_KEY,
    ...over,
  };
}

async function globalEnv(over: Partial<Env> = {}): Promise<Env> {
  return {
    RELEASES: bindingHolding({ shop: JSON.stringify(await gateRecord()) }),
    OCEL_ORIGIN: awsOrigin(),
    OCEL_SLUG: "",
    OCEL_PREVIEW: "1",
    OCEL_PREVIEW_GLOBAL: "1",
    OCEL_PREVIEW_BASE_DOMAIN: "preview.ocel.app",
    OCEL_PREVIEW_KEY: PREVIEW_KEY,
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
    new Request(`https://${host}/.ocel/login`, {
      method: "POST",
      headers: { authorization: basic(PASSWORD) },
    }),
    env,
  );
  expect(response.status).toBe(204);
  return (response.headers.get("set-cookie") ?? "").split(";", 1)[0];
}

describe("the gate on a project's preview worker", () => {
  it("refuses a request without credentials, with a login form no cache may keep", async () => {
    const { response, wire } = await visit(
      new Request(`https://${PROJECT_HOST}/users`),
      await projectEnv(),
    );

    expect(response.status).toBe(401);
    expect(response.headers.get("cache-control")).toBe("private, no-store");
    expect(response.headers.get("x-robots-tag")).toBe("noindex");
    expect(await response.text()).toContain('id="password"');
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
    expect(wire.calls[0].headers.get("cookie") ?? "").not.toContain("__Host-ocel-session");
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
        headers: { authorization: "Bearer abc", "x-ocel-bypass": BYPASS },
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
          headers: { "x-ocel-bypass": secret },
        }),
        env,
      );
      expect(response.status).toBe(200);
      expect(wire.calls[0].headers.get("x-ocel-bypass")).toBeNull();
    }
  });

  it("answers a bypass request that asks for a cookie with a redirect carrying it", async () => {
    const { response, wire } = await visit(
      new Request(`https://${PROJECT_HOST}/users`, {
        headers: { "x-ocel-bypass": BYPASS, "x-ocel-set-bypass-cookie": "true" },
      }),
      await projectEnv(),
    );

    expect(response.status).toBe(303);
    expect(response.headers.get("set-cookie")).toContain("__Host-ocel-session=");
    expect(wire.calls).toHaveLength(0);
  });

  it("refuses a request without credentials after one with credentials was served", async () => {
    const env = await projectEnv();
    const admitted = await visit(
      new Request(`https://${PROJECT_HOST}/users`, {
        headers: { "x-ocel-bypass": BYPASS },
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
        headers: { "x-ocel-bypass": BYPASS },
      }),
      await projectEnv({ RELEASES: bindingHolding({ p1: "{not json" }) }),
    );

    expect(response.status).toBe(503);
    expect(wire.calls).toHaveLength(0);
  });

  it("serves a preview with no gate configured", async () => {
    const { response } = await visit(
      new Request(`https://${PROJECT_HOST}/users`),
      await projectEnv({ RELEASES: bindingHolding({}) }),
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

  it("names the router on every response the gate answers itself", async () => {
    const env = await projectEnv();
    const answered = [
      await visit(new Request(`https://${PROJECT_HOST}/.well-known/ocel-edge`), env),
      await visit(
        new Request(`https://${PROJECT_HOST}/.ocel/login`, {
          method: "POST",
          headers: { authorization: basic(PASSWORD) },
        }),
        env,
      ),
      await visit(
        new Request(`https://${PROJECT_HOST}/users`, {
          headers: { "x-ocel-bypass": BYPASS, "x-ocel-set-bypass-cookie": "true" },
        }),
        env,
      ),
      await visit(
        new Request(`https://${PROJECT_HOST}/users`),
        await projectEnv({ RELEASES: bindingHolding({ p1: "{not json" }) }),
      ),
    ];

    expect(answered.map(({ response }) => response.status)).toEqual([401, 204, 303, 503]);
    for (const { response, wire } of answered) {
      expect(response.headers.get("x-ocel-router")).toBe("cloudflare");
      expect(wire.calls).toHaveLength(0);
    }
  });
});

describe("the gate on a project's production worker", () => {
  const CACHED = "cached body";
  const nextRecord = makeRecord({
    app: "web",
    framework: "next",
    routeTable: { format: "next", key: routeTableKey() },
    functionUrls: { "/": FN_URL },
    assetPrefix: "prod/p1/web/deploy-1/assets",
    isrPrefix: "prod/p1/web/deploy-1/isr",
  });
  const table: NextRouteTable = {
    rootFunction: "/",
    buildId: "deploy-1",
    basePath: "",
    pathnames: [],
    routes: {
      beforeMiddleware: [],
      beforeFiles: [],
      afterFiles: [],
      dynamicRoutes: [],
      onMatch: [],
      fallback: [],
    },
    dispatch: {},
  };

  async function productionEnv(over: Partial<Env> = {}): Promise<Env> {
    return {
      RELEASES: bindingHolding({ p1: JSON.stringify(await gateRecord()) }),
      OCEL_ORIGIN: awsOrigin(),
      OCEL_SLUG: "p1",
      ...over,
    };
  }

  async function cachedEnv(): Promise<Env> {
    return productionEnv({
      RELEASES: {
        ...bindingHolding({ p1: JSON.stringify(await gateRecord()) }),
        readPointerRecord: async () => ({
          kind: "record",
          release: "deploy-1",
          record: nextRecord,
        }),
      },
      ...storeOrigin({ url: "https://objects.example.com/ocel-acme-production" }),
    });
  }

  function cachedServe() {
    const asked: string[] = [];
    const ctx = contextWithExports({
      Serve: serveAnswering((request) => {
        const key = decodeURIComponent(new URL(request.url).pathname.slice(1));
        asked.push(key);
        return key === routeTableKey()
          ? new Response(JSON.stringify(table))
          : new Response(CACHED, { headers: { "cache-control": "public, max-age=31536000" } });
      }),
    });
    return { asked, ctx };
  }

  async function warm(url: string): Promise<void> {
    await caches.default.put(
      new Request(url),
      new Response(CACHED, { headers: { "cache-control": "public, max-age=31536000" } }),
    );
  }

  it("refuses a request without credentials, with a login form no cache may keep", async () => {
    const { response, wire } = await visit(
      new Request("https://app.example.com/users"),
      await productionEnv(),
    );

    expect(response.status).toBe(401);
    expect(response.headers.get("cache-control")).toBe("private, no-store");
    expect(response.headers.get("x-robots-tag")).toBe("noindex");
    expect(await response.text()).toContain('id="password"');
    expect(wire.calls).toHaveLength(0);
  });

  it("refuses a request without credentials for a response the edge cache holds", async () => {
    const url = "https://app.example.com/_next/static/chunks/app.js";
    await warm(url);
    const { asked, ctx } = cachedServe();

    const response = await worker.fetch(new Request(url), await cachedEnv(), ctx);

    expect(response.status).toBe(401);
    expect(response.headers.get("cache-control")).toBe("private, no-store");
    expect(await response.text()).not.toContain(CACHED);
    expect(asked).toHaveLength(0);
  });

  it("serves the response the edge cache holds to a request with the password", async () => {
    const url = "https://app.example.com/_next/static/chunks/app.js";
    await warm(url);
    const { ctx } = cachedServe();

    const response = await worker.fetch(
      new Request(url, { headers: { authorization: basic(PASSWORD) } }),
      await cachedEnv(),
      ctx,
    );

    expect(response.status).toBe(200);
    expect(await response.text()).toBe(CACHED);
    expect(response.headers.get("x-robots-tag")).toBe("noindex");
  });

  it("serves the page to the password, the session cookie and a bypass secret", async () => {
    const env = await productionEnv();
    const cookie = await login(env, "app.example.com");
    for (const headers of [
      { authorization: basic(PASSWORD) },
      { cookie },
      { "x-ocel-bypass": BYPASS },
    ]) {
      const { response, wire } = await visit(
        new Request("https://app.example.com/users", { headers }),
        env,
      );
      expect(response.status).toBe(200);
      expect(await response.text()).toBe("origin");
      expect(wire.calls[0].headers.get("x-ocel-bypass")).toBeNull();
      expect(wire.calls[0].headers.get("authorization") ?? "").not.toContain("Basic");
    }
  });

  it("gates the workers.dev host it serves when it has no domain", async () => {
    const { response, wire } = await visit(
      new Request("https://ocel-web.acct.workers.dev/users"),
      await productionEnv({ OCEL_SERVES_WORKERS_DEV: "1" }),
    );

    expect(response.status).toBe(401);
    expect(wire.calls).toHaveLength(0);
  });

  it("does not admit the preview password", async () => {
    const productionGate = await gateRecord({
      key: "production-key",
      passwordHash: await hashPassword("production password"),
      bypassSecrets: [],
    });
    const env = await productionEnv({
      RELEASES: bindingHolding({ p1: JSON.stringify(productionGate) }),
    });

    for (const headers of [{ authorization: basic(PASSWORD) }, { "x-ocel-bypass": BYPASS }]) {
      const { response, wire } = await visit(
        new Request("https://app.example.com/users", { headers }),
        env,
      );
      expect(response.status).toBe(401);
      expect(wire.calls).toHaveLength(0);
    }
  });

  it("serves a production with no gate configured, and leaves it indexable", async () => {
    const { response } = await visit(
      new Request("https://app.example.com/users"),
      await productionEnv({ RELEASES: bindingHolding({}) }),
    );

    expect(response.status).toBe(200);
    expect(response.headers.get("x-robots-tag")).toBeNull();
  });

  it("refuses every request when the gate cannot be read, before the edge cache", async () => {
    const url = "https://app.example.com/_next/static/chunks/app.js";
    await warm(url);
    const { asked, ctx } = cachedServe();
    const env = await cachedEnv();

    const response = await worker.fetch(
      new Request(url, { headers: { "x-ocel-bypass": BYPASS } }),
      { ...env, RELEASES: { ...env.RELEASES, readGate: async () => "{not json" } },
      ctx,
    );

    expect(response.status).toBe(503);
    expect(response.headers.get("cache-control")).toBe("private, no-store");
    expect(response.headers.get("x-robots-tag")).toBe("noindex");
    expect(asked).toHaveLength(0);
  });
});

describe("the gate on the shared preview entry", () => {
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
      passwordHash: await hashPassword("x"),
    });
    const env = await globalEnv({ RELEASES: bindingHolding({ shop: JSON.stringify(other) }) });

    const { response } = await visit(
      new Request(`https://${GLOBAL_HOST}/users`, { headers: { authorization: basic(PASSWORD) } }),
      env,
    );

    expect(response.status).toBe(401);
  });

  it("serves a project that has no recorded keys", async () => {
    const { response } = await visit(
      new Request(`https://${GLOBAL_HOST}/users`),
      await globalEnv({ RELEASES: bindingHolding({ other: "{}" }) }),
    );

    expect(response.status).toBe(200);
  });

  it("refuses every request when the releases store cannot be asked for the keys", async () => {
    const { response, wire } = await visit(
      new Request(`https://${GLOBAL_HOST}/users`),
      await globalEnv({ RELEASES: bindingHolding({ shop: new Error("store down") }) }),
    );

    expect(response.status).toBe(503);
    expect(wire.calls).toHaveLength(0);
  });
});

describe("the gate keys as the Go side encodes them", () => {
  for (const example of gateEncodings.gates) {
    it(`reads ${example.name} from the releases store`, async () => {
      expect(await readGate(bindingHolding({ p1: example.encoded }), "p1", () => 0)).toEqual({
        kind: "gated",
        gate: example.gate,
      });
    });
  }

  it("refuses a request without credentials rather than answering 503", async () => {
    const { response } = await visit(
      new Request(`https://${PROJECT_HOST}/users`),
      await projectEnv({
        RELEASES: bindingHolding({ p1: gateEncodings.gates[0]?.encoded ?? "" }),
      }),
    );

    expect(response.status).toBe(401);
  });
});

describe("a gate published to the releases store takes effect while the worker runs", () => {
  it("reads a rotated key once the cached read lapses", async () => {
    let held = JSON.stringify(await gateRecord());
    const releases: ReleasesBinding = {
      ...answerEveryRecordWith(answer),
      readGate: async () => held,
    };
    const before = await readGate(releases, "p1", () => 0);
    held = JSON.stringify(
      await gateRecord({
        key: "rotated",
        passwordHash: await hashPassword(PASSWORD),
      }),
    );

    const cached = await readGate(releases, "p1", () => 1_000);
    const lapsed = await readGate(releases, "p1", () => 6_000);

    expect(before.kind === "gated" && before.gate.key).toBe(GATE_KEY);
    expect(cached).toEqual(before);
    expect(lapsed.kind === "gated" && lapsed.gate.key).toBe("rotated");
  });

  it("asks the store once for the reads inside the cache window", async () => {
    let asked = 0;
    const releases: ReleasesBinding = {
      ...answerEveryRecordWith(answer),
      readGate: async () => {
        asked++;
        return undefined;
      },
    };

    await readGate(releases, "p1", () => 1_000);
    await readGate(releases, "p1", () => 2_000);
    await readGate(releases, "p1", () => 7_000);

    expect(asked).toBe(2);
  });

  it("does not cache a read that failed", async () => {
    let asked = 0;
    const releases: ReleasesBinding = {
      ...answerEveryRecordWith(answer),
      readGate: async () => {
        asked++;
        throw new Error("store down");
      },
    };

    expect((await readGate(releases, "p1", () => 1_000)).kind).toBe("unreadable");
    expect((await readGate(releases, "p1", () => 1_001)).kind).toBe("unreadable");
    expect(asked).toBe(2);
  });

  it("keeps an open project open on its last read while the store fails, as its releases are", async () => {
    let down = false;
    const releases: ReleasesBinding = {
      ...answerEveryRecordWith(answer),
      readGate: async () => {
        if (down) throw new Error("store down");
        return undefined;
      },
    };
    await readGate(releases, "p1", () => 0);
    down = true;

    expect(await readGate(releases, "p1", () => 60_000)).toEqual({ kind: "open" });
  });

  it("keeps a gated project gated on its last read while the store fails", async () => {
    let down = false;
    const releases: ReleasesBinding = {
      ...answerEveryRecordWith(answer),
      readGate: async () => {
        if (down) throw new Error("store down");
        return JSON.stringify(await gateRecord());
      },
    };
    const before = await readGate(releases, "p1", () => 0);
    down = true;

    expect(await readGate(releases, "p1", () => 60_000)).toEqual(before);
  });

  it("asks the store again on the next request after a failed read", async () => {
    let down = false;
    let asked = 0;
    const releases: ReleasesBinding = {
      ...answerEveryRecordWith(answer),
      readGate: async () => {
        asked++;
        if (down) throw new Error("store down");
        return undefined;
      },
    };
    await readGate(releases, "p1", () => 0);
    down = true;

    await readGate(releases, "p1", () => 60_000);
    await readGate(releases, "p1", () => 60_001);

    expect(asked).toBe(3);
  });
});

type VectorRequest = {
  method: string;
  host: string;
  target: string;
  headers: Record<string, string | string[]>;
};

function requestOf({ method, host, target, headers }: VectorRequest): Request {
  const sent = new Headers();
  for (const [name, raw] of Object.entries(headers)) {
    for (const value of [raw].flat()) sent.append(name, value);
  }
  return new Request(`https://${host}${target}`, { method, headers: sent, redirect: "manual" });
}

function fetchCannotCarry(request: VectorRequest): string {
  const carried = requestOf(request);
  const url = new URL(carried.url);
  if (carried.method !== request.method) return "Fetch normalises the method";
  if (url.pathname + url.search !== request.target) return "Fetch percent-encodes the target";
  if ([request.headers.cookie ?? []].flat().length > 1)
    return "Fetch joins cookie headers with a comma";
  return "";
}

describe("the entry worker's gate answers the gate's conformance vectors", () => {
  for (const example of vectors.cases) {
    const uncarried = fetchCannotCarry(example.request);
    it.skipIf(uncarried !== "")(
      `${example.name}${uncarried && ` (skipped: ${uncarried})`}`,
      async () => {
        const gate = { ...vectors.gate, ...("gate" in example ? example.gate : {}) };
        const expected = example.expect as {
          forward?: boolean;
          forwardHeaders?: Record<string, string>;
          forwardHeadersAbsent?: string[];
          responseHeaders?: Record<string, string>;
          status?: number;
          headers?: Record<string, string>;
          headersAbsent?: string[];
          bodyContains?: string[];
          bodyExcludes?: string[];
        };

        const outcome = await gateRequest(requestOf(example.request), gate, vectors.nowSeconds);

        if (expected.forward) {
          if (outcome.kind !== "forward")
            throw new Error(`answered ${outcome.response.status}, want the request forwarded`);
          for (const [name, want] of Object.entries(expected.forwardHeaders ?? {})) {
            expect(outcome.request.headers.get(name), name).toBe(want);
          }
          for (const name of expected.forwardHeadersAbsent ?? []) {
            expect(outcome.request.headers.get(name), name).toBeNull();
          }
          for (const [name, want] of Object.entries(expected.responseHeaders ?? {})) {
            expect(outcome.responseHeaders.get(name), name).toBe(want);
          }
          return;
        }

        if (outcome.kind !== "respond")
          throw new Error(`forwarded the request, want ${expected.status}`);
        expect(outcome.response.status).toBe(expected.status);
        for (const [name, want] of Object.entries(expected.headers ?? {})) {
          expect(outcome.response.headers.get(name), name).toBe(want);
        }
        for (const name of expected.headersAbsent ?? []) {
          expect(outcome.response.headers.get(name), name).toBeNull();
        }
        const body = await outcome.response.text();
        for (const want of expected.bodyContains ?? []) expect(body).toContain(want);
        for (const unwanted of expected.bodyExcludes ?? []) expect(body).not.toContain(unwanted);
      },
    );
  }
});
