import { describe, expect, test } from "vitest";
import { SubstrateError } from "../src/errors.mjs";
import {
  assetKey,
  assetPath,
  identity,
  imageConfigKey,
  sanitizeAppName,
} from "../src/keys.mjs";

const ID = { slug: "proj1", app: "web", buildId: "build-1" };

describe("config key", () => {
  // Must stay identical to imageConfigKey in cloud/aws/deploy/assets.go: this
  // function reads what that one wrote, and the prefix is deliberately outside
  // assets/, which is the app's public web root.
  test("is under image-config/, never under assets/", () => {
    expect(imageConfigKey(ID)).toBe("image-config/proj1/web/build-1.json");
    expect(imageConfigKey(ID).startsWith("assets/")).toBe(false);
  });
});

describe("asset key", () => {
  test("is the build prefix joined with the request pathname", () => {
    expect(assetKey(ID, "/logo.png")).toBe("assets/proj1/web/build-1/logo.png");
    expect(assetKey(ID, "/nested/dir/logo.png")).toBe(
      "assets/proj1/web/build-1/nested/dir/logo.png",
    );
  });

  test("decodes what the browser encoded", () => {
    expect(assetKey(ID, "/my%20logo.png")).toBe("assets/proj1/web/build-1/my logo.png");
  });
});

// Every one of these would otherwise name an object outside this build's prefix,
// which on a bucket holding every app in the account is another tenant's data.
describe("traversal guard", () => {
  const escapes = [
    "/../../other/build/secret.png",
    // The one a URL parser does not collapse, because it is not a path
    // separator until decodeURIComponent has run.
    "/%2e%2e%2f%2e%2e%2fsecret.png",
    "/a/%2e%2e/b.png",
    "/./x.png",
    "/a//b.png",
    "/",
  ];
  for (const pathname of escapes) {
    test(`refuses ${pathname}`, () => {
      expect(() => assetPath(pathname)).toThrow(SubstrateError);
    });
  }

  test("refuses a path that is not absolute", () => {
    expect(() => assetPath("logo.png")).toThrow(SubstrateError);
  });

  test("refuses an undecodable path", () => {
    expect(() => assetPath("/a%zz.png")).toThrow(SubstrateError);
  });

  test("refuses a NUL, which truncates a key downstream", () => {
    expect(() => assetPath("/logo.png%00.txt")).toThrow(SubstrateError);
  });
});

describe("identity", () => {
  test("refuses a separator in any component", () => {
    expect(() => identity({ ...ID, slug: "proj1/../other" })).toThrow(SubstrateError);
    expect(() => identity({ ...ID, buildId: "../build-2" })).toThrow(SubstrateError);
    expect(() => identity({ ...ID, slug: "" })).toThrow(SubstrateError);
  });

  test("refuses a dot run even without a separator", () => {
    expect(() => identity({ ...ID, buildId: "..." })).toThrow(SubstrateError);
  });

  // The uploader lowered the app name before writing the key and the worker's
  // OCEL_APP carries the raw one, so this is where they are made to agree —
  // deriving the key from the raw name would simply miss the object.
  test("sanitizes the app name the way the uploader did", () => {
    expect(sanitizeAppName("Web")).toBe("web");
    expect(sanitizeAppName("my app")).toBe("my-app");
    expect(sanitizeAppName("my___app")).toBe("my-app");
    expect(sanitizeAppName("-lead-and-trail-")).toBe("lead-and-trail");
    expect(sanitizeAppName("!!!")).toBe("ocel-worker");
    expect(sanitizeAppName("a".repeat(80))).toBe("a".repeat(63));
    expect(identity({ ...ID, app: "My App" }).app).toBe("my-app");
  });

  // A traversal attempt in the app name cannot survive sanitization, but the
  // segment check runs on the sanitized value anyway rather than trusting that.
  test("a traversal attempt in the app name sanitizes to a plain segment", () => {
    expect(identity({ ...ID, app: "../../etc" }).app).toBe("etc");
  });
});
