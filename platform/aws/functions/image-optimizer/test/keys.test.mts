import { describe, expect, test } from "vitest";
import { BootstrapError } from "../src/errors.mjs";
import { assetKey, assetPath, imageConfigKey, releaseAssetPrefix } from "../src/keys.mjs";

const PREFIX = "prod/proj1/web/r3f8a1c9d/assets";

describe("config key", () => {
  test("is the release's own sibling of the assets, never under them", () => {
    expect(imageConfigKey(PREFIX)).toBe("prod/proj1/web/r3f8a1c9d/image-config.json");
    expect(imageConfigKey(PREFIX).startsWith(PREFIX)).toBe(false);
  });
});

describe("asset key", () => {
  test("is the prefix the deploy minted joined with the request pathname", () => {
    expect(assetKey(PREFIX, "/logo.png")).toBe(`${PREFIX}/logo.png`);
    expect(assetKey(PREFIX, "/nested/dir/logo.png")).toBe(`${PREFIX}/nested/dir/logo.png`);
  });

  test("decodes what the browser encoded", () => {
    expect(assetKey(PREFIX, "/my%20logo.png")).toBe(`${PREFIX}/my logo.png`);
  });
});

describe("traversal guard", () => {
  const escapes = [
    "/../../other/build/secret.png",
    "/%2e%2e%2f%2e%2e%2fsecret.png",
    "/a/%2e%2e/b.png",
    "/./x.png",
    "/a//b.png",
    "/",
  ];
  for (const pathname of escapes) {
    test(`refuses ${pathname}`, () => {
      expect(() => assetPath(pathname)).toThrow(BootstrapError);
    });
  }

  test("refuses a path that is not absolute", () => {
    expect(() => assetPath("logo.png")).toThrow(BootstrapError);
  });

  test("refuses an undecodable path", () => {
    expect(() => assetPath("/a%zz.png")).toThrow(BootstrapError);
  });

  test("refuses a NUL, which truncates a key downstream", () => {
    expect(() => assetPath("/logo.png%00.txt")).toThrow(BootstrapError);
  });
});

describe("asset prefix", () => {
  test("takes the prefix the deploy minted", () => {
    expect(releaseAssetPrefix(PREFIX)).toBe(PREFIX);
  });

  test("refuses a prefix that does not end in the assets segment", () => {
    expect(() => releaseAssetPrefix("prod/proj1/web/r3f8a1c9d")).toThrow(BootstrapError);
    expect(() => releaseAssetPrefix("prod/proj1/web/r3f8a1c9d/isr")).toThrow(BootstrapError);
  });

  test("refuses a traversal in any segment", () => {
    expect(() => releaseAssetPrefix("prod/proj1/web/../assets")).toThrow(BootstrapError);
    expect(() => releaseAssetPrefix("prod/proj1/web/.../assets")).toThrow(BootstrapError);
    expect(() => releaseAssetPrefix("prod//web/r3f8a1c9d/assets")).toThrow(BootstrapError);
  });

  test("refuses a missing or empty prefix", () => {
    expect(() => releaseAssetPrefix(undefined)).toThrow(BootstrapError);
    expect(() => releaseAssetPrefix("")).toThrow(BootstrapError);
    expect(() => releaseAssetPrefix(42)).toThrow(BootstrapError);
  });
});
