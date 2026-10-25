import { IMAGE_ERROR } from "@framework/next-protocol/image";
import sharp from "sharp";
import { beforeEach, expect, test } from "vitest";
import { resetConfigMemo } from "../src/config.mjs";
import { newInProcessImageOrigin } from "../src/in-process-origin.mjs";
import { imageConfig, payload, storeWithConfig } from "../test-support/fixtures.mjs";
import { solid } from "../test-support/images.mjs";

beforeEach(() => resetConfigMemo());

test("an image the store holds is optimized in the serving process and answered as a response", async () => {
  const config = imageConfig();
  const store = storeWithConfig(config);
  store.put("assets/proj1/prod/web/r3f8a1c9d/logo.png", { bytes: await solid("png", 400, 200) });

  const response = await newInProcessImageOrigin(store)(payload(config, { w: 64 }));

  expect(response.status).toBe(200);
  const resized = await sharp(Buffer.from(await response.arrayBuffer())).metadata();
  expect(resized.width).toBe(64);
});

test("an image the store does not hold is answered as the optimizer's refusal", async () => {
  const config = imageConfig();

  const response = await newInProcessImageOrigin(storeWithConfig(config))(payload(config));

  expect(response.status).toBeGreaterThanOrEqual(400);
  expect(response.headers.get(IMAGE_ERROR)).toBe("1");
});
