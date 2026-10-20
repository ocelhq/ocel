import { describe, expect, test } from "vitest";
import { BootstrapError } from "../src/errors.mjs";
import { assetKey, assetPath, imageConfigKey, readRequestStorage } from "../src/keys.mjs";

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

describe("the storage a request names", () => {
  test("is the release whose assets and image config it reads", () => {
    expect(readRequestStorage(RELEASE)).toEqual(RELEASE);
  });

  test("refuses a coordinate that names no one release", () => {
    for (const storage of [
      undefined,
      "assets/proj1/prod/web/r3f8a1c9d",
      { ...RELEASE, release: "" },
      { ...RELEASE, env: ".." },
      { ...RELEASE, app: "web/../other" },
    ]) {
      expect(() => readRequestStorage(storage)).toThrow(BootstrapError);
    }
  });
});
