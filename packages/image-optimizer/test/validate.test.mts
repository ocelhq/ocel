import { describe, expect, test } from "vitest";
import { validate } from "../src/validate.mjs";
import { imageConfig, payload } from "./fixtures.mjs";

// The PR 3 validation table, re-run here against the config this function
// loaded. The edge already asserts it against real Next output; what these
// assert is that the authority-holding side reaches the same answers, so a
// worker running a stale manifest cannot serve something the loaded config
// forbids.
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
    // Arrays reach this side as JSON arrays rather than as repeated query keys,
    // but they answer the row Next answers. The presence check above it is
    // falsy-only — a non-empty array is truthy and falls through to here — which
    // is what makes this row reachable at all, exactly as w and q do it.
    rejects(
      { url: ["/a.png", "/b.png"] as unknown as string },
      '"url" parameter cannot be an array',
    );
  });

  test("too long", () => {
    rejects({ url: `/${"a".repeat(3072)}.png` }, '"url" parameter is too long');
  });

  // Before the relative/absolute branch, always. The other order is a full
  // allowlist bypass: //evil.example is a host the absolute branch checks and
  // the relative branch does not.
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

  // Next 16's localPatterns default carries search:"", so any query string on a
  // local url is a rejection rather than an allowance.
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
    // The scheme that would read this function's own filesystem.
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

  // hostname, never host: userinfo is how an allowlisted name gets hung off a
  // host the attacker controls.
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

  // Next interleaves the two parameters' checks, and the fixtures pin the
  // interleaving: both of q's presence checks run before w's value checks, so a
  // bad width with no quality is a quality error.
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

// Next throws on these and answers a bare 500. Reproduced as a status rather
// than as a throw: an uncaught exception here would be an unauthenticated crash.
describe("urls no parser can handle", () => {
  // The edge's own fixtures list these as query values (`url=%2F%25`), which
  // its query parser has already decoded to `/%` by the time the payload is
  // built — so `/%` is what actually arrives here for both.
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
