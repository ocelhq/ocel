import { createHmac } from "node:crypto";
import { expect, test } from "vitest";
import { isRefreshTaskSignedBy, signRefreshTask } from "../src/next/refresh-signature.mjs";

const body = Buffer.from('{"isrPrefix":"p","refresh":{}}');

test("a refresh task signed with a secret is signed by that secret", () => {
  const signature = signRefreshTask("s1", body);

  expect(signature).toBe(createHmac("sha256", "s1").update(body).digest("hex"));
  expect(isRefreshTaskSignedBy("s1", body, signature)).toBe(true);
});

test("a refresh task signed with another secret is not signed by this one", () => {
  expect(isRefreshTaskSignedBy("s1", body, signRefreshTask("s2", body))).toBe(false);
});

test("a refresh task changed after it was signed is not signed by anyone", () => {
  const signature = signRefreshTask("s1", body);
  const changed = Buffer.from(body);
  changed[0] = changed[0]! ^ 1;

  expect(isRefreshTaskSignedBy("s1", changed, signature)).toBe(false);
});

test("a signature that is missing, repeated or not a sha256 in hex signs nothing", () => {
  const signature = signRefreshTask("s1", body);

  expect(isRefreshTaskSignedBy("s1", body, undefined)).toBe(false);
  expect(isRefreshTaskSignedBy("s1", body, [signature, signature])).toBe(false);
  expect(isRefreshTaskSignedBy("s1", body, "zz".repeat(32))).toBe(false);
  expect(isRefreshTaskSignedBy("s1", body, signature.slice(1))).toBe(false);
  expect(isRefreshTaskSignedBy("s1", body, signature.toUpperCase())).toBe(false);
  expect(isRefreshTaskSignedBy("s1", body, `${signature}, ${signature}`)).toBe(false);
});
