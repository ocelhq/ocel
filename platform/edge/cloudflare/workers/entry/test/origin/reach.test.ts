import type { OriginReach } from "@platform/edge-contract/origin-reach";
import { describe, expect, it } from "vitest";
import { openOrigin } from "../../src/origin/reach";
import {
  awsOrigin,
  capturing,
  certificateBinding,
  storeOrigin,
  withGlobalFetch,
} from "../origin-deps";

const forbiddenFetch = (async () => {
  throw new Error("global fetch must not be called");
}) as typeof fetch;

function signedBy(origin: Partial<OriginReach>): string {
  return JSON.stringify({
    reach: {
      kind: "sigv4",
      service: "lambda",
      region: "ap-south-1",
      accessKeyId: "AKIAEXAMPLE",
      secretAccessKey: "secretkey",
    },
    throttle: { status: 429 },
    ...origin,
  });
}

describe("an origin reached by signing requests", () => {
  it("signs a request to any host with the service and region the origin names", async () => {
    const wire = capturing();
    const { fetch: originFetch } = openOrigin({ OCEL_ORIGIN: signedBy({}) });

    await withGlobalFetch(wire.fetch, () => originFetch(new Request("https://fn.example.com/x")));

    const authorization = wire.calls[0].headers.get("authorization") ?? "";
    expect(authorization).toMatch(
      /^AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE\/\d{8}\/ap-south-1\/lambda\/aws4_request, /,
    );
  });

  it("is never reached through a client certificate the worker happens to hold", async () => {
    const wire = capturing();
    const { binding, sent } = certificateBinding();
    const { fetch: originFetch } = openOrigin({
      OCEL_ORIGIN: signedBy({}),
      OCEL_ORIGIN_CLIENT_CERTIFICATE: binding,
    });

    await withGlobalFetch(wire.fetch, () => originFetch(new Request("https://fn.example.com/x")));

    expect(sent).toEqual([]);
    expect(wire.calls).toHaveLength(1);
  });
});

describe("an origin reached by a client certificate", () => {
  it("is reached through the certificate binding, with no signature", async () => {
    const { binding, sent } = certificateBinding();
    const { fetch: originFetch } = openOrigin({
      ...storeOrigin(),
      OCEL_ORIGIN_CLIENT_CERTIFICATE: binding,
    });

    await withGlobalFetch(forbiddenFetch, () =>
      originFetch(new Request("https://d-abc.origin.example.com/x")),
    );

    expect(sent).toHaveLength(1);
    expect(sent[0].headers.has("authorization")).toBe(false);
    expect(sent[0].headers.has("x-amz-date")).toBe(false);
  });

  it("is refused when the worker holds no certificate binding to reach it with", () => {
    const { OCEL_ORIGIN } = storeOrigin();

    expect(() => openOrigin({ OCEL_ORIGIN })).toThrow(/OCEL_ORIGIN_CLIENT_CERTIFICATE/);
  });
});

describe("a malformed origin", () => {
  it.each([
    ["is not JSON", "{reach"],
    ["is not an object", "[]"],
    ["names a way to reach it no edge knows", signedBy({ reach: { kind: "carrier-pigeon" } })],
    [
      "signs with no region",
      signedBy({
        reach: { kind: "sigv4", service: "lambda", accessKeyId: "k", secretAccessKey: "s" },
      }),
    ],
    ["names no throttle status", signedBy({ throttle: {} as OriginReach["throttle"] })],
    [
      "names a store with no URL",
      signedBy({
        store: {
          region: "auto",
          holdsEntries: false,
          accessKeyId: "k",
          secretAccessKey: "s",
        } as OriginReach["store"],
      }),
    ],
    ["names a negative image request limit", signedBy({ images: { maxRequestBytes: -1 } })],
  ])("is refused when it %s", (_, raw) => {
    expect(() => openOrigin({ OCEL_ORIGIN: raw })).toThrow(/^ocel: OCEL_ORIGIN /);
  });

  it("is refused when the worker holds none", () => {
    expect(() => openOrigin({} as { OCEL_ORIGIN: string })).toThrow(/^ocel: OCEL_ORIGIN /);
  });
});

describe("the origin's throttle", () => {
  it("is the status with the header the origin names", async () => {
    const { isThrottle } = openOrigin({ OCEL_ORIGIN: awsOrigin() });
    const lambda = new Response(null, {
      status: 429,
      headers: { "x-amzn-errortype": "TooManyRequestsException" },
    });

    expect(await isThrottle(lambda)).toBe(true);
    expect(await isThrottle(new Response(null, { status: 429 }))).toBe(false);
    expect(
      await isThrottle(new Response(null, { status: 503, headers: { "x-amzn-errortype": "x" } })),
    ).toBe(false);
  });

  it("is the status with the body text the origin names", async () => {
    const { isThrottle } = openOrigin(storeOrigin());
    const cloudRunAbort = "The request was aborted because there was no available instance.";

    expect(await isThrottle(new Response(cloudRunAbort, { status: 429 }))).toBe(true);
    expect(await isThrottle(new Response("slow down", { status: 429 }))).toBe(false);
  });

  it("is any answer with the status when the origin names nothing more", async () => {
    const { isThrottle } = openOrigin({ OCEL_ORIGIN: signedBy({ throttle: { status: 503 } }) });

    expect(await isThrottle(new Response(null, { status: 503 }))).toBe(true);
    expect(await isThrottle(new Response(null, { status: 429 }))).toBe(false);
  });
});

describe("the origin's store", () => {
  const key = "pages/p1/prod/web/r9a0c0e00/blog/a.cache.json";

  it("reads a page entry under the entries prefix of a store that holds entries", async () => {
    const wire = capturing();
    const { entries } = openOrigin(storeOrigin({ holdsEntries: true, entriesPrefix: "entries/" }));

    const read = await withGlobalFetch(wire.fetch, async () => (await entries?.get(key))?.text());

    expect(read).toBe("origin");
    expect(wire.calls.map((call) => call.url)).toEqual([
      `https://objects.example.com/ocel-acme-production/entries/${key}`,
    ]);
    expect(wire.calls[0].headers.get("authorization")).toMatch(
      /^AWS4-HMAC-SHA256 Credential=GOOG1EXAMPLEACCESSID\/\d{8}\/auto\/s3\/aws4_request, /,
    );
  });

  it("reads page entries at the store's root when it names no entries prefix", async () => {
    const wire = capturing();
    const { entries } = openOrigin({ OCEL_ORIGIN: awsOrigin({ bucket: "assets" }) });

    await withGlobalFetch(wire.fetch, async () => entries?.get(key));

    expect(wire.calls.map((call) => call.url)).toEqual([
      `https://assets.s3.eu-west-2.amazonaws.com/${key}`,
    ]);
  });

  it("opens no entry source on a store that holds no entries", () => {
    expect(openOrigin(storeOrigin()).entries).toBeUndefined();
  });

  it("opens neither objects nor entries for an origin that names no store", () => {
    const origin = openOrigin({ OCEL_ORIGIN: awsOrigin() });

    expect(origin.objects).toBeUndefined();
    expect(origin.entries).toBeUndefined();
  });

  it("reads an object under the store's prefix", async () => {
    const wire = capturing();
    const { objects } = openOrigin(storeOrigin());

    await withGlobalFetch(wire.fetch, async () => objects?.readObject("prod/a.js"));

    expect(wire.calls.map((call) => call.url)).toEqual([
      "https://objects.example.com/ocel-acme-production/assets/prod/a.js",
    ]);
  });

  it("is opened once for each origin the worker holds", () => {
    const env = storeOrigin({ holdsEntries: true });

    expect(openOrigin(env).entries).toBe(openOrigin(env).entries);
  });
});

describe("the origin's images", () => {
  it("are what the origin names", () => {
    expect(
      openOrigin({ OCEL_ORIGIN: awsOrigin({ optimizerUrl: "https://img.example/" }) }).images,
    ).toEqual({ optimizerUrl: "https://img.example/", maxRequestBytes: 6225920 });
    expect(openOrigin(storeOrigin()).images).toEqual({ maxRequestBytes: 33554432 });
    expect(openOrigin({ OCEL_ORIGIN: awsOrigin() }).images).toBeUndefined();
  });
});
