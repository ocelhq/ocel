import { describe, expect, test } from "vitest";
import { validate } from "../src/validate.mjs";
import { imageConfig, payload } from "./fixtures.mjs";

function check(overrides: Parameters<typeof payload>[1], config = imageConfig()) {
  return validate(payload(config, overrides), config);
}

function rejects(overrides: Parameters<typeof payload>[1], message: string, config?: ReturnType<typeof imageConfig>) {
  const result = check(overrides, config);
  expect(result.ok).toBe(false);
  if (result.ok) return;
  expect(result.status).toBe(400);
  expect(result.message).toBe(message);
}

describe("url", () => {
  test("required", () => {
    rejects({ url: "" }, '"url" parameter is required');
  });

  test("cannot be an array", () => {
    rejects(
      { url: ["/a.png", "/b.png"] as unknown as string },
      '"url" parameter cannot be an array',
    );
  });

  test("too long", () => {
    rejects({ url: `/${"a".repeat(3072)}.png` }, '"url" parameter is too long');
  });

  test("protocol-relative", () => {
    rejects(
      { url: "//evil.example/x.png" },
      '"url" parameter cannot be a protocol-relative URL (//)',
    );
  });

  test("recursive, including percent-encoded", () => {
    rejects({ url: "/_next/image?url=/a.png" }, '"url" parameter cannot be recursive');
    rejects({ url: "/_next/%69mage/a.png" }, '"url" parameter cannot be recursive');
    rejects({ url: "/assetprefix/_next/image/a.png" }, '"url" parameter cannot be recursive');
  });

  test("relative and outside localPatterns", () => {
    const config = imageConfig({
      localPatterns: [{ pathname: "^\\/assets\\/.*$", search: "" }],
    });
    rejects({ url: "/elsewhere/a.png" }, '"url" parameter is not allowed', config);
    expect(check({ url: "/assets/a.png" }, config).ok).toBe(true);
  });

  test("relative with a query string, under the Next 16 default", () => {
    rejects({ url: "/a.png?v=1" }, '"url" parameter is not allowed');
  });

  test("an empty localPatterns array denies every local path", () => {
    rejects({ url: "/a.png" }, '"url" parameter is not allowed', imageConfig({ localPatterns: [] }));
  });

  test("an absent localPatterns allows every local path", () => {
    const config = imageConfig({ localPatterns: undefined });
    expect(check({ url: "/anything/at/all.png?v=1" }, config).ok).toBe(true);
  });

  test("absolute and unparseable", () => {
    rejects({ url: "http://" }, '"url" parameter is invalid');
  });

  test("absolute and not http(s)", () => {
    rejects({ url: "ftp://cdn.example.com/a.png" }, '"url" parameter is invalid');
    rejects({ url: "data:image/png;base64,AAAA" }, '"url" parameter is invalid');
    rejects({ url: "file:///etc/passwd" }, '"url" parameter is invalid');
  });

  test("absolute and outside the allowlist", () => {
    rejects({ url: "https://evil.example/a.png" }, '"url" parameter is not allowed');
  });

  test("the compiled hostname regex is anchored, so a suffix attack fails", () => {
    const config = imageConfig({
      remotePatterns: [
        { protocol: "https", hostname: "^(?:(?!\\.)(?:[^.]*)\\.)example\\.com$", pathname: "^\\/.*$" },
      ],
    });
    rejects({ url: "https://cdn.example.com.evil.example/a.png" }, '"url" parameter is not allowed', config);
    expect(check({ url: "https://cdn.example.com/a.png" }, config).ok).toBe(true);
  });

  test("userinfo cannot smuggle an allowlisted hostname", () => {
    rejects({ url: "https://cdn.example.com@evil.example/a.png" }, '"url" parameter is not allowed');
  });

  test("domains matches an exact hostname", () => {
    const config = imageConfig({ domains: ["images.example.org"], remotePatterns: [] });
    expect(check({ url: "https://images.example.org/a.png" }, config).ok).toBe(true);
    rejects({ url: "https://sub.images.example.org/a.png" }, '"url" parameter is not allowed', config);
  });
});

describe("width and quality", () => {
  test("w required", () => {
    rejects({ w: undefined as unknown as number }, '"w" parameter (width) is required');
  });

  test("w cannot be an array", () => {
    rejects({ w: [640, 750] as unknown as number }, '"w" parameter (width) cannot be an array');
  });

  test("w must be an integer", () => {
    rejects({ w: 99.9 }, '"w" parameter (width) must be an integer greater than 0');
    rejects({ w: "640" as unknown as number }, '"w" parameter (width) must be an integer greater than 0');
  });

  test("w not in deviceSizes union imageSizes", () => {
    rejects({ w: 641 }, '"w" parameter (width) of 641 is not allowed');
    expect(check({ w: 384 }).ok).toBe(true);
  });

  test("q required", () => {
    rejects({ q: undefined as unknown as number }, '"q" parameter (quality) is required');
  });

  test("q cannot be an array", () => {
    rejects({ q: [75, 80] as unknown as number }, '"q" parameter (quality) cannot be an array');
  });

  test("q must be an integer between 1 and 100", () => {
    rejects({ q: 75.5 }, '"q" parameter (quality) must be an integer between 1 and 100');
    rejects({ q: 0 }, '"q" parameter (quality) must be an integer between 1 and 100', imageConfig({ qualities: undefined }));
    rejects({ q: 101 }, '"q" parameter (quality) must be an integer between 1 and 100', imageConfig({ qualities: undefined }));
  });

  test("q not in qualities", () => {
    rejects({ q: 80 }, '"q" parameter (quality) of 80 is not allowed');
  });

  test("w=0 with no q is a quality error, not a width error", () => {
    rejects(
      { w: 0, q: undefined as unknown as number },
      '"q" parameter (quality) is required',
    );
  });

  test("w=99.9 with no q is a width error", () => {
    rejects(
      { w: 99.9, q: undefined as unknown as number },
      '"w" parameter (width) must be an integer greater than 0',
    );
  });

  test("w=0 with a valid q is a width error", () => {
    rejects({ w: 0 }, '"w" parameter (width) must be an integer greater than 0');
  });
});

describe("urls no parser can handle", () => {
  for (const url of ["/%", "/a%zz", "/%E0%A4%A"]) {
    test(`${url} is a controlled 500`, () => {
      const result = check({ url });
      expect(result.ok).toBe(false);
      if (result.ok) return;
      expect(result.status).toBe(500);
      expect(result.message).toBe("Internal Server Error");
    });
  }
});

describe("a valid request", () => {
  test("yields the normalized href and the parsed numbers", () => {
    const result = check({ url: "https://cdn.example.com/a.png", w: 1080, q: 75 });
    expect(result).toEqual({
      ok: true,
      params: {
        href: "https://cdn.example.com/a.png",
        width: 1080,
        quality: 75,
        isAbsolute: true,
      },
    });
  });

  test("marks a relative url as local", () => {
    const result = check({ url: "/logo.png" });
    expect(result.ok && result.params.isAbsolute).toBe(false);
  });
});
