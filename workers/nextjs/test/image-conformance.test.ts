import { describe, expect, it } from "vitest";

import fixtures from "./fixtures/image-conformance.json";
import {
  getSupportedMimeType,
  serveImage,
  unprovisionedImageOrigin,
  type ImageConfig,
} from "../src/image";

// The differential oracle: every case in test/fixtures/image-conformance.json
// is what the real Next server in examples/next-test answered, recorded by
// scripts/generate-image-fixtures.mjs. Regenerate deliberately —
// `pnpm fixtures:image` — so a Next bump lands as a reviewable diff.

// The two 400s Next raises only after fetching the source. They belong to the
// optimizer origin, not to this tier; the edge's job on those rows is to let
// the request through.
const ORIGIN_REJECTIONS = new Set([
  '"url" parameter is valid but image type is not allowed',
  "The requested resource isn't a valid image.",
]);

// The registered divergences (docs/research/image-optimization-design.md,
// "Deliberate divergences from Next"). Each names the row it applies to and
// asserts what we do *instead* of what the fixture recorded, so a divergence is
// pinned rather than absorbed by the generic branch below.
const DIVERGENCES: Record<
  string,
  (fixture: FixtureCase, response: Response, config: ImageConfig) => void
> = {
  // Divergence 6. @hapi/accept throws on a parameter with no value, so Next
  // answers this Accept header with a 500 before it ever looks at the image.
  // The edge only needs the negotiated type as a cache-key component, so it
  // declines to negotiate and lets the request reach the origin, which raises
  // its own uncached 500 if it comes to that.
  "accept-malformed-parameter": (fixture, response, config) => {
    expect(fixture.status).toBe(500);
    expect(getSupportedMimeType(config.formats, fixture.request.accept ?? "")).toBe("");
    expect(response.status).toBe(502);
  },
};

interface FixtureCase {
  name: string;
  note?: string;
  request: { search: string; accept: string | null };
  status: number;
  body: { text?: string; bytes?: number; sha256?: string };
  contentType: string | null;
  cacheControl: string | null;
  vary: string | null;
  contentDisposition: string | null;
  csp: string | null;
}

const variants = fixtures.variants as unknown as Array<{
  variant: string;
  config: ImageConfig;
  cases: FixtureCase[];
}>;

function requestFor(fixture: FixtureCase): { url: URL; request: Request } {
  const url = new URL(`https://app.example/_next/image?${fixture.request.search}`);
  const headers = new Headers();
  if (fixture.request.accept !== null) headers.set("accept", fixture.request.accept);
  return { url, request: new Request(url, { headers }) };
}

for (const { variant, config, cases } of variants) {
  describe(`next ${fixtures.next} conformance (${variant})`, () => {
    for (const fixture of cases) {
      const rejectedAtTheEdge =
        fixture.status === 400 && !ORIGIN_REJECTIONS.has(fixture.body.text ?? "");

      const divergence = DIVERGENCES[fixture.name];

      it(`${fixture.name}${fixture.note ? ` — ${fixture.note}` : ""}`, async () => {
        const { url, request } = requestFor(fixture);
        const response = await serveImage(request, url, {
          config,
          basePath: "",
          slug: "fixture",
          app: "fixture",
          buildId: "fixture",
          // Reaching the origin at all is the assertion for every row Next did
          // not reject at the edge; what the origin then does is PR 5's.
          origin: unprovisionedImageOrigin,
        });

        if (divergence) {
          divergence(fixture, response, config);
        } else if (rejectedAtTheEdge) {
          expect(response.status).toBe(fixture.status);
          expect(await response.text()).toBe(fixture.body.text);
          expect(response.headers.get("content-type")).toBe(fixture.contentType);
        } else {
          expect(response.status).toBe(502);
        }
      });
    }

    // The undocumented guard, asserted against real output: a wildcard-only
    // Accept negotiates nothing, so the response keeps the source format. Every
    // 200 over a PNG source is covered, which is both wildcard rows, the absent
    // header, an unconfigured format, a q=0 exclusion, and the browser header.
    describe("format negotiation against the served content-type", () => {
      const pngCases = cases.filter(
        (fixture) =>
          fixture.status === 200 &&
          new URLSearchParams(fixture.request.search).get("url")?.endsWith(".png"),
      );

      it("covers every Accept variant in the matrix", () => {
        expect(pngCases.map((fixture) => fixture.name)).toEqual(
          expect.arrayContaining([
            "accept-absent",
            "accept-any",
            "accept-image-wildcard",
            "accept-browser",
            "accept-webp-only",
            "accept-avif-only",
          ]),
        );
      });

      for (const fixture of pngCases) {
        it(`${fixture.name} serves ${fixture.contentType}`, () => {
          // The output type is the negotiated one, or — a PNG source being a
          // known extension that is neither webp nor avif — the source's own.
          const negotiated = getSupportedMimeType(
            config.formats,
            fixture.request.accept ?? "",
          );
          expect(negotiated || "image/png").toBe(fixture.contentType);
        });
      }
    });
  });
}

describe("registered divergences", () => {
  it("each name a row of every variant, so a renamed fixture cannot drop one", () => {
    for (const { variant, cases } of variants) {
      const names = cases.map((fixture) => fixture.name);
      for (const registered of Object.keys(DIVERGENCES)) {
        expect(names, variant).toContain(registered);
      }
    }
  });
});
