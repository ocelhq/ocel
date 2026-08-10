import { beforeEach, expect, test } from "vitest";
import { resetConfigMemo } from "../src/config.mjs";
import { handle } from "../src/index.mjs";
import { imageConfig, payload, storeWithConfig } from "./fixtures.mjs";
import { solid } from "./images.mjs";

beforeEach(() => resetConfigMemo());

function event(body: unknown, overrides: Record<string, unknown> = {}) {
  return {
    requestContext: { http: { method: "POST" } },
    body: typeof body === "string" ? body : JSON.stringify(body),
    ...overrides,
  };
}

test("answers a POST of the edge's payload", async () => {
  const config = imageConfig();
  const store = storeWithConfig(config);
  store.put("assets/proj1/web/build-1/logo.png", { bytes: await solid("png", 300, 150) });
  const response = await handle(event(payload(config)), { store });
  expect(response.status).toBe(200);
  expect(response.headers["content-type"]).toBe("image/webp");
});

test("decodes a base64 body, which is how a Function URL may deliver it", async () => {
  const config = imageConfig();
  const store = storeWithConfig(config);
  store.put("assets/proj1/web/build-1/logo.png", { bytes: await solid("png", 300, 150) });
  const response = await handle(
    event(Buffer.from(JSON.stringify(payload(config))).toString("base64"), {
      isBase64Encoded: true,
    }),
    { store },
  );
  expect(response.status).toBe(200);
});

test("refuses a method other than POST", async () => {
  const response = await handle({ requestContext: { http: { method: "GET" } } });
  expect(response.status).toBe(405);
});

test("an unreadable envelope is a bare 400", async () => {
  const store = storeWithConfig(imageConfig());
  for (const body of ["", "not json", "[]", "null", '"string"', "42"]) {
    const response = await handle(event(body), { store });
    expect(response.status).toBe(400);
    expect(response.headers).toEqual({});
  }
  expect(store.reads).toEqual([]);
});

test("an envelope missing its fields is refused without a read", async () => {
  const store = storeWithConfig(imageConfig());
  const response = await handle(event({ url: "/logo.png" }), { store });
  expect(response.status).toBe(502);
  expect(store.reads).toEqual([]);
});

test("an unconfigured function is a 502", async () => {
  const previous = process.env["OCEL_IMAGE_ASSET_BUCKET"];
  delete process.env["OCEL_IMAGE_ASSET_BUCKET"];
  try {
    const response = await handle(event(payload(imageConfig())));
    expect(response.status).toBe(502);
  } finally {
    if (previous !== undefined) process.env["OCEL_IMAGE_ASSET_BUCKET"] = previous;
  }
});
