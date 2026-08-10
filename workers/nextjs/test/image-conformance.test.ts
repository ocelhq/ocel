import { describe, expect, it } from "vitest";

import fixtures from "./fixtures/image-conformance.json";
import {
  getSupportedMimeType,
  serveImage,
  unprovisionedImageOrigin,
  type ImageConfig,
} from "../src/image";

const ORIGIN_REJECTIONS = new Set([
  '"url" parameter is valid but image type is not allowed',
  "The requested resource isn't a valid image.",
]);

const DIVERGENCES: Record<
  string,
  (fixture: FixtureCase, response: Response, config: ImageConfig) => void
> = {
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
