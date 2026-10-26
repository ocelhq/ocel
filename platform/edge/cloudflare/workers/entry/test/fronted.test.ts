import { createExecutionContext } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import type { Env } from "../src/env";
import { resolveServe, type ServeFetch } from "../src/index";
import { openOrigin } from "../src/origin/reach";
import type { StoredRecord } from "../src/releases";
import { Serve } from "../src/serve";
import {
  buildOriginCall,
  parseRenderCall,
  RENDER_PROPS,
  RENDER_RELEASE_HEADER,
  SERVE_REFUSED_HEADER,
} from "../src/serve-key";
import { servedNextRelease } from "./next-release";
import {
  answerEveryRecordWith,
  awsOrigin,
  capturing,
  makeRecord,
  withGlobalFetch,
} from "./origin-deps";
import { resolveBase } from "./resolve-base";
import { fakeServing } from "./serving-deps";

const HOST = "shop.example.com";

const OWN_ORIGIN = "http://10.0.3.7";

function frontedRecord(over: Partial<StoredRecord> = {}): StoredRecord {
  return makeRecord({
    app: "web",
    functionUrls: {},
    origin: OWN_ORIGIN,
    fronted: true,
    ...over,
  });
}

async function frontedServe(record: StoredRecord, cachesContainers: boolean) {
  const served = fakeServing();
  const wire = capturing();
  const serve = (await resolveServe(
    {
      binding: answerEveryRecordWith(async () => ({ kind: "record", release: "r1", record })),
      slug: "p1",
      host: HOST,
      app: "web",
    },
    resolveBase({
      originFetchFor: () => wire.fetch,
      serving: { ...resolveBase().serving, serve: served.serving.serve },
      cachesContainers,
    }),
  )) as ServeFetch;
  return { serve, served: served.calls, wire };
}

describe("a fronted release", () => {
  it("is passed to the hostname it was asked for, not an origin of its own", async () => {
    const { serve, wire } = await frontedServe(frontedRecord(), true);

    await serve(new Request(`https://${HOST}/catalog?page=2`, { method: "POST" }));

    expect(wire.calls.map(({ url }) => url)).toEqual([`https://${HOST}/catalog?page=2`]);
  });

  it("is rendered through Serve by a worker no project told to cache containers, as the shared preview entry is", async () => {
    const { serve, served } = await frontedServe(frontedRecord(), false);

    await serve(new Request(`https://${HOST}/catalog`));

    expect(served).toHaveLength(1);
  });

  it("is rendered through Serve at the hostname it was asked for when the worker caches containers", async () => {
    const { serve, served } = await frontedServe(frontedRecord(), true);

    await serve(new Request(`https://${HOST}/catalog`));

    expect(served).toHaveLength(1);
    expect(served[0].originUrl).toBe(`https://${HOST}/catalog`);
  });

  it("dispatches a fronted Next build's function routes to the hostname it was asked for", async () => {
    const wire = capturing();
    const serve = servedNextRelease({
      table: { dispatch: { "/api": { kind: "function", id: "/" } } },
      record: { functionUrls: { "/": OWN_ORIGIN }, fronted: true },
      fetch: wire.fetch,
    });

    await serve(new Request(`https://${HOST}/api`));

    expect(wire.calls.map(({ url }) => url)).toEqual([`https://${HOST}/api`]);
  });
});

describe("a Next container release no route table routes, behind a worker that caches containers", () => {
  it("is kept under the app's own cache headers, as any container is", async () => {
    const { serve, served, wire } = await frontedServe(frontedRecord({ framework: "next" }), true);

    await serve(new Request(`https://${HOST}/`));

    expect(served).toHaveLength(1);
    expect(wire.calls).toHaveLength(0);
  });
});

describe("the origin reach of a fronted release", () => {
  const env = { OCEL_ORIGIN: awsOrigin() } as Pick<Env, "OCEL_ORIGIN">;

  it("asks the hostname unsigned, past the zone's cache, carrying the visitor's address", async () => {
    const wire = capturing();
    await withGlobalFetch(wire.fetch, () =>
      openOrigin(env).fetchFor({ fronted: true })(`https://${HOST}/`, {
        headers: { "x-ocel-client-address": "203.0.113.9" },
      }),
    );

    expect(wire.calls).toHaveLength(1);
    expect(wire.calls[0].headers.has("authorization")).toBe(false);
    expect(wire.calls[0].headers.get("cf-connecting-ip")).toBe("203.0.113.9");
    expect(wire.calls[0].cache).toBe("no-store");
  });
});

describe("Serve rendering a fronted release", () => {
  function serveFor(record: StoredRecord): Serve {
    const ctx = Object.assign(createExecutionContext(), { props: RENDER_PROPS });
    return new Serve(ctx, {
      RELEASES: { readServedRecord: async () => ({ kind: "record", record }) },
      OCEL_ORIGIN: awsOrigin(),
    } as unknown as Env);
  }

  function renderAt(origin: string): Request {
    const { url } = buildOriginCall(`${origin}/catalog`, {
      host: HOST,
      app: "web",
      release: "r00000001",
    });
    return new Request(url, {
      headers: {
        [RENDER_RELEASE_HEADER]: JSON.stringify({
          slug: "p1",
          storageCoordinate: "p1/prod/api/r00000001",
          tags: [],
          honoursAppCacheHeaders: true,
        }),
      },
    });
  }

  it("renders at the hostname it was asked for", async () => {
    const wire = capturing();
    const response = await withGlobalFetch(wire.fetch, () =>
      serveFor(frontedRecord()).fetch(renderAt(`https://${HOST}`)),
    );

    expect(response.headers.has(SERVE_REFUSED_HEADER)).toBe(false);
    expect(wire.calls.map(({ url }) => url)).toEqual([`https://${HOST}/catalog`]);
    expect(parseRenderCall(renderAt(`https://${HOST}`).url).host).toBe(HOST);
  });

  it("refuses to render at any other origin", async () => {
    const response = await serveFor(frontedRecord()).fetch(
      renderAt("https://elsewhere.example.com"),
    );

    expect(response.headers.has(SERVE_REFUSED_HEADER)).toBe(true);
  });
});
