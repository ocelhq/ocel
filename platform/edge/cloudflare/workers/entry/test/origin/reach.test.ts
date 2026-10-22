import type { OriginReach } from "@platform/edge-contract/origin-reach";
import { describe, expect, it } from "vitest";
import { openOrigin } from "../../src/origin/reach";
import {
  awsOrigin,
  capturing,
  makeRecord,
  secretOrigin,
  storeOrigin,
  withGlobalFetch,
} from "../origin-deps";

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
    const originFetch = openOrigin({ OCEL_ORIGIN: signedBy({}) }).fetchFor(makeRecord());

    await withGlobalFetch(wire.fetch, () => originFetch(new Request("https://fn.example.com/x")));

    const authorization = wire.calls[0].headers.get("authorization") ?? "";
    expect(authorization).toMatch(
      /^AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE\/\d{8}\/ap-south-1\/lambda\/aws4_request, /,
    );
  });
});

describe("an origin reached by presenting its origin secret", () => {
  const rotating = {
    secret: "current",
    previousSecret: "previous",
    previousFingerprint: "6da0633528deaa01",
  };

  it("presents the current secret to a release that holds it", async () => {
    const wire = capturing();
    const origin = openOrigin({ OCEL_ORIGIN: secretOrigin(rotating) });

    await withGlobalFetch(wire.fetch, () =>
      origin.fetchFor(makeRecord({ originSecretFingerprint: "97b0560280ed60a5" }))(
        new Request("https://r1---web.a.run.app/"),
      ),
    );

    expect(wire.calls[0].headers.get("x-ocel-origin-secret")).toBe("current");
  });

  it("presents the previous secret to a release that holds the one the rotation replaced", async () => {
    const wire = capturing();
    const origin = openOrigin({ OCEL_ORIGIN: secretOrigin(rotating) });

    await withGlobalFetch(wire.fetch, () =>
      origin.fetchFor(makeRecord({ originSecretFingerprint: "6da0633528deaa01" }))(
        new Request("https://r1---web.a.run.app/"),
      ),
    );

    expect(wire.calls[0].headers.get("x-ocel-origin-secret")).toBe("previous");
  });

  it("is refused when it names no secret", () => {
    expect(() => openOrigin({ OCEL_ORIGIN: secretOrigin({ secret: "" }) })).toThrow(
      /^ocel: OCEL_ORIGIN /,
    );
  });
});

describe("a visitor's origin secret", () => {
  const forged = { "x-ocel-origin-secret": "a-guess" };

  it("never reaches an origin reached by signing requests", async () => {
    const wire = capturing();
    const originFetch = openOrigin({ OCEL_ORIGIN: signedBy({}) }).fetchFor(makeRecord());

    await withGlobalFetch(wire.fetch, () =>
      originFetch(new Request("https://fn.example.com/x", { headers: forged })),
    );

    expect(wire.calls[0].headers.has("x-ocel-origin-secret")).toBe(false);
  });

  it("is replaced by the origin's own on an origin reached by its origin secret", async () => {
    const wire = capturing();
    const originFetch = openOrigin({ OCEL_ORIGIN: secretOrigin({ secret: "current" }) }).fetchFor(
      makeRecord(),
    );

    await withGlobalFetch(wire.fetch, () =>
      originFetch(new Request("https://r1---web.a.run.app/", { headers: forged })),
    );

    expect(wire.calls[0].headers.get("x-ocel-origin-secret")).toBe("current");
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
    [
      "presents only a previous secret",
      signedBy({
        reach: {
          kind: "origin-secret",
          previousSecret: "s1",
          previousFingerprint: "6da0633528deaa01",
        },
      }),
    ],
    [
      "presents a previous secret with no fingerprint",
      signedBy({ reach: { kind: "origin-secret", secret: "s2", previousSecret: "s1" } }),
    ],
    [
      "names a previous fingerprint with no secret",
      signedBy({
        reach: { kind: "origin-secret", secret: "s2", previousFingerprint: "6da0633528deaa01" },
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

  it("reads a page entry at its own key from a store that holds entries", async () => {
    const wire = capturing();
    const { entries } = openOrigin(storeOrigin({ holdsEntries: true }));

    const read = await withGlobalFetch(wire.fetch, async () => (await entries?.get(key))?.text());

    expect(read).toBe("origin");
    expect(wire.calls.map((call) => call.url)).toEqual([
      `https://objects.example.com/ocel-acme-production/${key}`,
    ]);
    expect(wire.calls[0].headers.get("authorization")).toMatch(
      /^AWS4-HMAC-SHA256 Credential=GOOG1EXAMPLEACCESSID\/\d{8}\/auto\/s3\/aws4_request, /,
    );
  });

  it("reads page entries at their own keys from an AWS origin's edge bucket", async () => {
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

  it("reads an object at its own key in the store's bucket", async () => {
    const wire = capturing();
    const { objects } = openOrigin(storeOrigin());

    await withGlobalFetch(wire.fetch, async () =>
      objects?.readObject("assets/shop/prod/web/r1a2b3c4d/a.js"),
    );

    expect(wire.calls.map((call) => call.url)).toEqual([
      "https://objects.example.com/ocel-acme-production/assets/shop/prod/web/r1a2b3c4d/a.js",
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
