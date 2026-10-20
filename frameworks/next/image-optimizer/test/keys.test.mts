import { describe, expect, test } from "vitest";
import { BootstrapError } from "../src/errors.mjs";
import { assetKey, assetPath, imageConfigKey, releaseOfAssetPrefix } from "../src/keys.mjs";

const PREFIX = "assets/proj1/prod/web/r3f8a1c9d";
const RELEASE = { project: "proj1", env: "prod", app: "web", release: "r3f8a1c9d" };

describe("config key", () => {
  test("is the release's image config, never under its assets", () => {
    expect(imageConfigKey(RELEASE)).toBe("image-config/proj1/prod/web/r3f8a1c9d/image-config.json");
    expect(imageConfigKey(RELEASE).startsWith(PREFIX)).toBe(false);
  });
});

describe("asset key", () => {
  test("is the release's assets prefix joined with the request pathname", () => {
    expect(assetKey(RELEASE, "/logo.png")).toBe(`${PREFIX}/logo.png`);
    expect(assetKey(RELEASE, "/nested/dir/logo.png")).toBe(`${PREFIX}/nested/dir/logo.png`);
  });

  test("decodes what the browser encoded", () => {
    expect(assetKey(RELEASE, "/my%20logo.png")).toBe(`${PREFIX}/my logo.png`);
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
  test("names the release whose assets it prefixes", () => {
    expect(releaseOfAssetPrefix(PREFIX)).toEqual(RELEASE);
  });

  test("refuses a prefix that is not one release's assets", () => {
    expect(() => releaseOfAssetPrefix("assets/proj1/prod/web")).toThrow(BootstrapError);
    expect(() => releaseOfAssetPrefix("pages/proj1/prod/web/r3f8a1c9d")).toThrow(BootstrapError);
    expect(() => releaseOfAssetPrefix("assets/proj1/prod/web/r3f8a1c9d/logo.png")).toThrow(
      BootstrapError,
    );
    expect(() => releaseOfAssetPrefix("prod/proj1/web/r3f8a1c9d/assets")).toThrow(BootstrapError);
  });

  test("refuses a traversal in any segment", () => {
    expect(() => releaseOfAssetPrefix("assets/proj1/prod/../r3f8a1c9d")).toThrow(BootstrapError);
    expect(() => releaseOfAssetPrefix("assets/proj1/prod/.../r3f8a1c9d")).toThrow(BootstrapError);
    expect(() => releaseOfAssetPrefix("assets/proj1//web/r3f8a1c9d")).toThrow(BootstrapError);
  });

  test("refuses a missing or empty prefix", () => {
    expect(() => releaseOfAssetPrefix(undefined)).toThrow(BootstrapError);
    expect(() => releaseOfAssetPrefix("")).toThrow(BootstrapError);
    expect(() => releaseOfAssetPrefix(42)).toThrow(BootstrapError);
  });
});
