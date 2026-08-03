import { createHash } from "node:crypto";
import { expect, test, vi } from "vitest";
import {
  compileImageConfig,
  imageConfigHash,
  serializeImageConfig,
} from "../src/image-config.mts";
import { defaultImages } from "./fixtures.mts";

type Images = Parameters<typeof compileImageConfig>[0];

function images(overrides: Record<string, unknown> = {}): Images {
  return { ...defaultImages, ...overrides } as unknown as Images;
}

function compile(overrides: Record<string, unknown> = {}) {
  const compiled = compileImageConfig(images(overrides));
  if (!compiled) throw new Error("expected a compiled image config");
  return compiled;
}

function remotePattern(pattern: Record<string, unknown> | URL) {
  return compile({ remotePatterns: [pattern] }).remotePatterns[0]!;
}

const matches = (source: string, value: string) =>
  new RegExp(source).test(value);

// The whole compiled shape for the config an app that configures nothing gets,
// literal regex sources included: the worker matches with `new RegExp` over
// exactly these, so a Next bump that changes what picomatch emits has to show
// up as a diff here rather than as a silent change in what is allowed.
test("compiles the default image config Next hands the adapter", () => {
  expect(compile()).toEqual({
    path: "/_next/image",
    deviceSizes: [640, 750, 828, 1080, 1200, 1920, 2048, 3840],
    imageSizes: [32, 48, 64, 96, 128, 256, 384],
    qualities: [75],
    formats: ["image/webp"],
    domains: [],
    minimumCacheTTL: 14400,
    maximumRedirects: 3,
    maximumResponseBody: 50000000,
    dangerouslyAllowSVG: false,
    dangerouslyAllowLocalIP: false,
    contentSecurityPolicy: "script-src 'none'; frame-src 'none'; sandbox;",
    contentDispositionType: "attachment",
    remotePatterns: [],
    localPatterns: [
      {
        pathname:
          "^(?:(?!(?:^|\\/)\\.{1,2}(?:\\/|$))(?:(?:(?!(?:^|\\/)\\.{1,2}(?:\\/|$)).)*?)\\/?)$",
        search: "",
      },
    ],
  });
});

// Unset localPatterns means "allow every local path" and unset qualities means
// "any quality in 1..100"; both have to reach the worker as absent keys, since a
// present-but-empty list would mean the opposite.
test("keeps unset localPatterns and qualities absent", () => {
  const compiled = compile({ localPatterns: undefined, qualities: undefined });

  expect("localPatterns" in compiled).toBe(false);
  expect("qualities" in compiled).toBe(false);
});

test("compiles remote pattern fields Next matches verbatim", () => {
  expect(
    remotePattern({
      protocol: "https",
      hostname: "images.example.com",
      port: "8080",
      pathname: "/media/**",
      search: "?v=1",
    }),
  ).toMatchObject({ protocol: "https", port: "8080", search: "?v=1" });
});

// A subdomain wildcard that can be satisfied to the right of the host it names
// is an allowlist bypass: any attacker-controlled host would pass.
test.each([
  ["example.com", false],
  ["a.example.com", true],
  ["a.b.example.com", true],
  ["example.com.evil.com", false],
  ["evilexample.com", false],
  ["a.example.com.", false],
])("*.example.com against %s is %s", (hostname, expected) => {
  expect(
    matches(remotePattern({ hostname: "*.example.com" }).hostname, hostname),
  ).toBe(expected);
});

test.each([
  ["example.com", false],
  ["a.example.com", true],
  ["a.b.example.com", true],
  ["example.com.evil.com", false],
  ["evilexample.com", false],
  ["a.example.com.", false],
])("**.example.com against %s is %s", (hostname, expected) => {
  expect(
    matches(remotePattern({ hostname: "**.example.com" }).hostname, hostname),
  ).toBe(expected);
});

test("a literal hostname matches nothing else", () => {
  const { hostname } = remotePattern({ hostname: "example.com" });

  expect(matches(hostname, "example.com")).toBe(true);
  expect(matches(hostname, "a.example.com")).toBe(false);
  expect(matches(hostname, "example.com.evil.com")).toBe(false);
});

// Pathnames are compiled with dot:true and hostnames without it — Next's own
// asymmetry, kept by construction: a dotfile path is servable, a leading-dot
// host label is not.
test("pathnames match dotfiles and hostnames do not match empty labels", () => {
  const { pathname, hostname } = remotePattern({
    hostname: "**",
    pathname: "/assets/**",
  });

  expect(matches(pathname, "/assets/.well-known/logo.png")).toBe(true);
  expect(matches(pathname, "/assets/nested/logo.png")).toBe(true);
  expect(matches(pathname, "/other/logo.png")).toBe(false);
  expect(matches(hostname, "a.example.com")).toBe(true);
  expect(matches(hostname, ".example.com")).toBe(false);
});

test("a single-star pathname matches one segment only", () => {
  const { pathname } = remotePattern({
    hostname: "example.com",
    pathname: "/a/*",
  });

  expect(matches(pathname, "/a/logo.png")).toBe(true);
  expect(matches(pathname, "/a/b/logo.png")).toBe(false);
});

test("an omitted pathname allows every path", () => {
  const { pathname } = remotePattern({ hostname: "example.com" });

  expect(matches(pathname, "/")).toBe(true);
  expect(matches(pathname, "/deeply/nested/logo.png")).toBe(true);
});

// A URL entry is legal in remotePatterns and would serialize to `{}`, taking
// the whole allowlist entry with it.
test("normalizes a URL remote pattern into matchable fields", () => {
  const compiled = remotePattern(
    new URL("https://images.example.com/media/**"),
  );

  expect(compiled).toMatchObject({ protocol: "https", port: "", search: "" });
  expect(matches(compiled.hostname, "images.example.com")).toBe(true);
  expect(matches(compiled.hostname, "images.example.com.evil.com")).toBe(false);
  expect(matches(compiled.pathname, "/media/logo.png")).toBe(true);
  expect(matches(compiled.pathname, "/other/logo.png")).toBe(false);
});

test("a URL remote pattern carries its explicit port", () => {
  expect(remotePattern(new URL("http://localhost:3000/**"))).toMatchObject({
    protocol: "http",
    port: "3000",
  });
});

test("configHash is stable across runs and independent of key order", () => {
  const a = imageConfigHash(compile());
  const b = imageConfigHash(compile());
  const reordered = imageConfigHash(
    JSON.parse(
      JSON.stringify(
        Object.fromEntries(Object.entries(compile()).reverse()),
      ),
    ),
  );

  expect(a).toBe(b);
  expect(reordered).toBe(a);
});

test.each([
  ["minimumCacheTTL", 60],
  ["qualities", [50]],
  ["formats", ["image/avif"]],
  ["domains", ["example.com"]],
  ["dangerouslyAllowSVG", true],
  ["contentDispositionType", "inline"],
  ["remotePatterns", [{ hostname: "*.example.com" }]],
  ["localPatterns", [{ pathname: "/assets/**", search: "" }]],
])("configHash changes when %s changes", (key, value) => {
  expect(imageConfigHash(compile({ [key]: value }))).not.toBe(
    imageConfigHash(compile()),
  );
});

test("the artifact bytes are what configHash is taken over", () => {
  const compiled = compile();

  expect(
    createHash("sha256").update(serializeImageConfig(compiled)).digest("hex"),
  ).toBe(imageConfigHash(compiled));
});

// Both are valid, common setups — an external CDN, a static-export-shaped app —
// and next/image never asks for /_next/image under either, so there is nothing
// to serve and nothing to fail the build over.
test.each([
  ["a non-default loader", { loader: "cloudinary" }, /images\.loader/],
  ["unoptimized images", { unoptimized: true }, /images\.unoptimized is true/],
])("warns and compiles nothing for %s", (_name, overrides, expected) => {
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

  expect(compileImageConfig(images(overrides))).toBeUndefined();
  expect(warn).toHaveBeenCalledWith(expect.stringMatching(expected));

  warn.mockRestore();
});
