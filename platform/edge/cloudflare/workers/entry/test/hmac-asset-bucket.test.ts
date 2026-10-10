import { afterEach, describe, expect, it } from "vitest";
import { newHmacAssetBucket, readHmacAssetBucket } from "../src/hmac-asset-bucket";

const credentials = { accessKeyId: "GOOG1EXAMPLEACCESSID", secretAccessKey: "examplesecret/key+" };

const config = {
  ...credentials,
  endpoint: "https://objects.example.com",
  bucket: "ocel-acme-production",
  prefix: "assets/",
};

const originalFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = originalFetch;
});

function answering(respond: (request: Request) => Response): Request[] {
  const sent: Request[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = new Request(input as RequestInfo, init);
    sent.push(request);
    return respond(request);
  }) as typeof fetch;
  return sent;
}

const encoder = new TextEncoder();

async function hmac(key: ArrayBuffer | string, data: string): Promise<ArrayBuffer> {
  const imported = await crypto.subtle.importKey(
    "raw",
    typeof key === "string" ? encoder.encode(key) : key,
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  return crypto.subtle.sign("HMAC", imported, encoder.encode(data));
}

function hex(buffer: ArrayBuffer): string {
  return [...new Uint8Array(buffer)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

async function sha256Hex(data: string): Promise<string> {
  return hex(await crypto.subtle.digest("SHA-256", encoder.encode(data)));
}

async function expectedSignature(request: Request, secret: string): Promise<string> {
  const authorization = request.headers.get("authorization") ?? "";
  const scope = /Credential=[^/]+\/(\d{8})\/([^/]+)\/([^/]+)\/aws4_request/.exec(authorization);
  const signedHeaders = /SignedHeaders=([^,]+)/.exec(authorization)?.[1] ?? "";
  const [, day, region, service] = scope ?? [];
  const amzDate = request.headers.get("x-amz-date") ?? "";
  const url = new URL(request.url);
  const canonicalHeaders = signedHeaders
    .split(";")
    .map((name) => `${name}:${name === "host" ? url.host : request.headers.get(name)}\n`)
    .join("");
  const canonical = [
    request.method,
    url.pathname,
    url.search.slice(1),
    canonicalHeaders,
    signedHeaders,
    request.headers.get("x-amz-content-sha256"),
  ].join("\n");
  const toSign = [
    "AWS4-HMAC-SHA256",
    amzDate,
    `${day}/${region}/${service}/aws4_request`,
    await sha256Hex(canonical),
  ].join("\n");
  let key = await hmac(`AWS4${secret}`, day);
  for (const part of [region, service, "aws4_request"]) key = await hmac(key, part);
  return hex(await hmac(key, toSign));
}

describe("an asset read from a bucket with an HMAC key", () => {
  it("is a path-style GET of the object under the assets store", async () => {
    const sent = answering(() => new Response("chunk"));

    await newHmacAssetBucket(config).get("prod/shop/web/r1/assets/_next/static/app.js");

    expect(sent).toHaveLength(1);
    expect(sent[0].method).toBe("GET");
    expect(sent[0].url).toBe(
      "https://objects.example.com/ocel-acme-production/assets/prod/shop/web/r1/assets/_next/static/app.js",
    );
  });

  it("is signed as AWS Signature Version 4 in the region auto for the service s3", async () => {
    const sent = answering(() => new Response("chunk"));

    await newHmacAssetBucket(config).get("prod/shop/web/r1/assets/logo.svg");

    const authorization = sent[0].headers.get("authorization") ?? "";
    expect(authorization).toMatch(
      /^AWS4-HMAC-SHA256 Credential=GOOG1EXAMPLEACCESSID\/\d{8}\/auto\/s3\/aws4_request, /,
    );
    expect(authorization).toContain("SignedHeaders=host;x-amz-content-sha256;x-amz-date,");
  });

  it("carries the signature an independent Signature Version 4 computation gives", async () => {
    const sent = answering(() => new Response("chunk"));

    await newHmacAssetBucket(config).get("prod/shop/web/r1/assets/logo.svg");

    const signature = /Signature=([0-9a-f]{64})$/.exec(sent[0].headers.get("authorization") ?? "");
    expect(signature?.[1]).toBe(await expectedSignature(sent[0], credentials.secretAccessKey));
  });

  it("encodes the characters of an object name once, in the url and in the signature", async () => {
    const sent = answering(() => new Response("page"));

    await newHmacAssetBucket(config).get("prod/shop/web/r1/assets/docs/[...slug] one.html");

    expect(new URL(sent[0].url).pathname).toBe(
      "/ocel-acme-production/assets/prod/shop/web/r1/assets/docs/%5B...slug%5D%20one.html",
    );
    const signature = /Signature=([0-9a-f]{64})$/.exec(sent[0].headers.get("authorization") ?? "");
    expect(signature?.[1]).toBe(await expectedSignature(sent[0], credentials.secretAccessKey));
  });

  it("never sends the secret", async () => {
    const sent = answering(() => new Response("chunk"));

    await newHmacAssetBucket(config).get("prod/shop/web/r1/assets/logo.svg");

    expect([...sent[0].headers.values()].join(" ")).not.toContain(credentials.secretAccessKey);
    expect(sent[0].url).not.toContain(credentials.secretAccessKey);
  });

  it("hands the router the body and the etag the bucket answers with", async () => {
    answering(() => new Response("chunk", { headers: { etag: '"abc"' } }));

    const object = await newHmacAssetBucket(config).get("a/b.js");

    expect(await new Response(object?.body).text()).toBe("chunk");
    expect(object?.httpEtag).toBe('"abc"');
  });

  it("is a miss where the object is absent", async () => {
    answering(() => new Response("<Error/>", { status: 404 }));

    expect(await newHmacAssetBucket(config).get("a/gone.js")).toBeNull();
  });

  it("is refused, naming the bucket and the status, where the bucket refuses the key", async () => {
    answering(() => new Response("<Error/>", { status: 403 }));

    await expect(newHmacAssetBucket(config).get("a/b.js")).rejects.toThrow(
      "ocel: the asset bucket ocel-acme-production answered 403 for assets/a/b.js",
    );
  });
});

describe("the store a worker is configured with", () => {
  const env = {
    OCEL_ASSET_STORE_ENDPOINT: "https://objects.example.com/",
    OCEL_ASSET_STORE_BUCKET: "ocel-acme-production",
    OCEL_ASSET_STORE_PREFIX: "assets/",
    OCEL_ASSET_STORE_ACCESS_KEY_ID: credentials.accessKeyId,
    OCEL_ASSET_STORE_SECRET_KEY: credentials.secretAccessKey,
  };

  it("reads assets at the endpoint and with the key it is given", async () => {
    const sent = answering(() => new Response("chunk"));

    await readHmacAssetBucket(env)?.get("a/b.js");

    expect(sent[0].url).toBe("https://objects.example.com/ocel-acme-production/assets/a/b.js");
  });

  it("is absent where the worker is given no endpoint", () => {
    expect(readHmacAssetBucket({ ...env, OCEL_ASSET_STORE_ENDPOINT: undefined })).toBeUndefined();
  });

  it("is absent where the worker is given no bucket", () => {
    expect(readHmacAssetBucket({ ...env, OCEL_ASSET_STORE_BUCKET: undefined })).toBeUndefined();
  });

  it("is absent where the worker is given no secret", () => {
    expect(readHmacAssetBucket({ ...env, OCEL_ASSET_STORE_SECRET_KEY: undefined })).toBeUndefined();
  });
});
