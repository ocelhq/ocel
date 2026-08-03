import { describe, expect, test, vi } from "vitest";
import { ImageError, SubstrateError } from "../src/errors.mjs";
import { sharp } from "../src/sharp.mjs";
import { AVIF, GIF, ICO, JPEG, PNG, SVG, WEBP } from "../src/sniff.mjs";
import { fallbackOr400, transform } from "../src/transform.mjs";
import { animatedGif, ico, solid, svg } from "./images.mjs";
import { imageConfig } from "./fixtures.mjs";

function input(bytes: Uint8Array, overrides: Record<string, unknown> = {}) {
  return {
    bytes,
    mimeType: "",
    width: 100,
    quality: 75,
    config: imageConfig(),
    ...overrides,
  } as Parameters<typeof transform>[0];
}

// AVIF is only a legal negotiated type for a config that lists it; the fixture's
// default formats is Next 16's own ["image/webp"].
function avifConfig() {
  return imageConfig({ formats: [AVIF, WEBP] });
}

// The rules run in the design's order, and the order is the security property.
// CVE-2025-55173 was an ordering defect: a bypass branch ahead of the
// not-an-image rejection let a payload that was never an image reach a response
// body under a type it chose.
describe("rule 1: not an image", () => {
  test("rejects bytes with no recognised signature", async () => {
    await expect(transform(input(new TextEncoder().encode("<!DOCTYPE html>")))).rejects.toThrow(
      "The requested resource isn't a valid image.",
    );
  });

  test("rejects an empty body", async () => {
    await expect(transform(input(new Uint8Array(0)))).rejects.toThrow(
      "The requested resource isn't a valid image.",
    );
  });

  // The ordering assertion itself: a PDF is a format libvips can load and one
  // Next's own sniffer names, so it is exactly the payload a bypass branch
  // running first would have returned unmodified.
  test("rejects a non-image before any bypass branch can return it", async () => {
    await expect(transform(input(new TextEncoder().encode("%PDF-1.7\n%%EOF")))).rejects.toThrow(
      "The requested resource isn't a valid image.",
    );
  });

  // The exploitable consequence of a wildcard sentinel spelled as 0x00: ICO's
  // signature degenerated to `bytes[2] === 0x01`, ICO is a BYPASS_TYPE, and so
  // each of these reached a 200 body byte-for-byte as image/x-icon.
  test("a payload that merely has 0x01 at byte 2 is not an ICO to bypass", async () => {
    for (const head of [
      [0x4d, 0x5a, 0x01, 0x00], // Windows PE
      [0x1f, 0x8b, 0x01, 0x00], // gzip
      [0x50, 0x4b, 0x01, 0x02], // ZIP local header
    ]) {
      await expect(transform(input(new Uint8Array([...head, 0, 0, 0, 0])))).rejects.toThrow(
        "The requested resource isn't a valid image.",
      );
    }
  });
});

describe("rule 2: SVG", () => {
  test("is rejected without dangerouslyAllowSVG", async () => {
    const error = await transform(input(svg())).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ImageError);
    expect((error as ImageError).status).toBe(400);
    expect((error as ImageError).message).toBe(
      '"url" parameter is valid but image type is not allowed',
    );
  });

  // With the flag on it becomes a bypass, not a transform: the bytes are relayed
  // untouched rather than handed to a rasterizer whose density an attacker would
  // otherwise influence.
  test("is passed through unmodified with the flag on", async () => {
    const bytes = svg();
    const result = await transform(
      input(bytes, { config: imageConfig({ dangerouslyAllowSVG: true }) }),
    );
    expect(result).toEqual({
      bytes,
      contentType: SVG,
      unmodified: true,
      passthrough: false,
    });
  });
});

describe("rules 3 and 4: unmodified", () => {
  test("an animated GIF is returned as it arrived", async () => {
    const bytes = animatedGif();
    const result = await transform(input(bytes));
    expect(result.bytes).toBe(bytes);
    expect(result.contentType).toBe(GIF);
    expect(result.unmodified).toBe(true);
    // Not a passthrough: these bytes are perfectly well served, so the edge
    // keeps the upstream's freshness for them rather than forcing the minimum.
    expect(result.passthrough).toBe(false);
  });

  test("a bypass type is returned as it arrived", async () => {
    const bytes = ico();
    const result = await transform(input(bytes));
    expect(result.bytes).toBe(bytes);
    expect(result.contentType).toBe(ICO);
    expect(result.unmodified).toBe(true);
    expect(result.passthrough).toBe(false);
  });
});

describe("rule 5: transform", () => {
  test("resizes to the requested width and re-encodes", async () => {
    const result = await transform(input(await solid("jpeg", 400, 300), { width: 100 }));
    expect(result.unmodified).toBe(false);
    expect(result.contentType).toBe(JPEG);
    const meta = await sharp(result.bytes).metadata();
    expect(meta.width).toBe(100);
    expect(meta.height).toBe(75);
  });

  // withoutEnlargement: a request for a width larger than the source must not
  // upscale, or a 16px favicon becomes a 3840px decode on request.
  test("never enlarges", async () => {
    const result = await transform(input(await solid("jpeg", 80, 40), { width: 3840 }));
    const meta = await sharp(result.bytes).metadata();
    expect(meta.width).toBe(80);
  });

  test("the negotiated mimeType wins over the source type", async () => {
    const result = await transform(input(await solid("jpeg"), { mimeType: WEBP }));
    expect(result.contentType).toBe(WEBP);
    expect((await sharp(result.bytes).metadata()).format).toBe("webp");
  });

  // Without a negotiation, a source that is itself an output-only format falls
  // back to JPEG rather than being handed back as a type the client never said
  // it accepts.
  test("a webp source with no negotiation becomes jpeg", async () => {
    const result = await transform(input(await solid("webp"), { mimeType: "" }));
    expect(result.contentType).toBe(JPEG);
  });

  test("a png source with no negotiation stays png", async () => {
    const result = await transform(input(await solid("png"), { mimeType: "" }));
    expect(result.contentType).toBe(PNG);
  });

  test("quality reaches the encoder", async () => {
    const low = await transform(input(await solid("jpeg", 400, 300), { quality: 10 }));
    const high = await transform(input(await solid("jpeg", 400, 300), { quality: 95 }));
    expect(low.bytes.byteLength).toBeLessThan(high.bytes.byteLength);
  });

  // AVIF at a given quality number is visually far ahead of JPEG at the same
  // number, so the app's quality is rescaled onto AVIF's curve rather than
  // spending encode time on fidelity nobody asked for.
  // The encoder call is asserted against a stub pipeline rather than by reading
  // an encoded file back: the rescale is a specific number, and a re-decode can
  // only show that some AVIF came out.
  async function avifCall(overrides: Record<string, unknown>) {
    // Built before the stub is installed — the fixture helper encodes through
    // the same sharp this is about to replace.
    const source = await solid("jpeg");
    const avif = vi.fn().mockReturnThis();
    const pipeline = {
      timeout: vi.fn().mockReturnThis(),
      rotate: vi.fn().mockReturnThis(),
      resize: vi.fn().mockReturnThis(),
      avif,
      webp: vi.fn().mockReturnThis(),
      png: vi.fn().mockReturnThis(),
      jpeg: vi.fn().mockReturnThis(),
      toBuffer: vi.fn().mockResolvedValue(Buffer.from([1, 2, 3])),
    };
    const module = await import("../src/sharp.mjs");
    const spy = vi
      .spyOn(module, "sharp")
      .mockReturnValue(pipeline as unknown as ReturnType<typeof sharp>);
    try {
      await transform(
        input(source, { mimeType: AVIF, config: avifConfig(), ...overrides }),
      );
      // Snapshotted before the restore, which clears the spy's own call record.
      // The stub pipeline's mocks are plain fns and keep theirs.
      return { source, avif, pipeline, sharpCalls: [...spy.mock.calls] };
    } finally {
      spy.mockRestore();
    }
  }

  test("AVIF quality is rescaled and effort pinned", async () => {
    const { source, avif, pipeline, sharpCalls } = await avifCall({ quality: 75 });
    // max(75 - 20, 1) === 55. Next 16.2.10's image-optimizer.js:885 — a flat
    // -20, not a proportional rescale.
    expect(avif).toHaveBeenCalledWith({ quality: 55, effort: 3 });
    // Exhaustively, because the dangerous settings here are the ones that are
    // dangerous by being present: `unlimited: true`, `failOn: "none"`,
    // `sequentialRead: false`, and any `density` derived from the request.
    // limitInputPixels is sharp's own default, stated rather than omitted.
    expect(sharpCalls).toEqual([[source, { limitInputPixels: 268402689 }]]);
    expect(pipeline.timeout).toHaveBeenCalledWith({ seconds: 7 });
    expect(pipeline.resize).toHaveBeenCalledWith(100, undefined, {
      withoutEnlargement: true,
    });
    // rotate() before resize(), so EXIF orientation is applied to the source
    // and the requested width is the width the browser sees.
    expect(pipeline.rotate.mock.invocationCallOrder[0]!).toBeLessThan(
      pipeline.resize.mock.invocationCallOrder[0]!,
    );
  });

  // Through transform(), so it is the source's own max() that is under test:
  // asserting the arithmetic in the test file would pass with the max() deleted.
  // q <= 20 is not reachable past validation with Next's default qualities, but
  // an app that lists a low quality makes it reachable, and avif({quality: 0})
  // is not a value the encoder should ever see.
  test("the AVIF rescale floors at 1, never at 0 or below", async () => {
    // Literal expectations, not the formula restated: an expectation that
    // recomputes max(q - 20, 1) would still hold with the source's max() deleted.
    for (const [quality, expected] of [
      [25, 5],
      [21, 1],
      [20, 1],
      [1, 1],
    ] as const) {
      const { avif } = await avifCall({ quality });
      expect(avif).toHaveBeenCalledWith({ quality: expected, effort: 3 });
    }
  });
});

// The negotiated type is echoed straight into Content-Type, so it is the one
// payload field that has to be checked against the loaded config rather than
// taken on the edge's word. Unchecked, `text/html` was a 200 Content-Type over
// JPEG bytes, and `image/avif` was honoured against a config listing only webp.
// Refused as a substrate failure, not a 400: only a malformed caller can produce
// it, and 502 is the status the edge will not cache.
describe("the negotiated mimeType", () => {
  test("is refused when the config no longer lists that format", async () => {
    await expect(
      transform(
        input(await solid("jpeg"), {
          mimeType: AVIF,
          config: imageConfig({ formats: [WEBP] }),
        }),
      ),
    ).rejects.toBeInstanceOf(SubstrateError);
  });

  test("is refused when it is not an image type at all", async () => {
    for (const mimeType of ["text/html", "application/javascript", "*/*", 42]) {
      await expect(
        transform(await solid("jpeg").then((b) => input(b, { mimeType }))),
      ).rejects.toBeInstanceOf(SubstrateError);
    }
  });

  test("an empty mimeType is no negotiation, not a refusal", async () => {
    const result = await transform(input(await solid("png"), { mimeType: "" }));
    expect(result.contentType).toBe(PNG);
  });
});

// The failure matrix. sharp throwing is not hypothetical here: the loader
// allowlist means a still GIF or an AVIF source cannot be loaded at all, which
// lands on exactly this path.
describe("failure behavior", () => {
  test("a source no allowed loader can read is served as its original bytes", async () => {
    const bytes = new Uint8Array([
      0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00, 0x80, 0x00, 0x00,
      0xff, 0xff, 0xff, 0x00, 0x00, 0x00, 0x2c, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00,
      0x01, 0x00, 0x00, 0x02, 0x02, 0x44, 0x01, 0x00, 0x3b,
    ]);
    const result = await transform(input(bytes));
    expect(result.bytes).toBe(bytes);
    expect(result.contentType).toBe(GIF);
    expect(result.unmodified).toBe(true);
    // The one flag that makes the edge force minimumCacheTTL.
    expect(result.passthrough).toBe(true);
  });

  test("truncated bytes of an allowed format fall back rather than 400", async () => {
    const whole = await solid("jpeg", 400, 300);
    const result = await transform(input(whole.subarray(0, 40)));
    expect(result.passthrough).toBe(true);
    expect(result.contentType).toBe(JPEG);
  });

  test("with no upstream type at all it is a 400", () => {
    expect(() => fallbackOr400(new Uint8Array([1]), null, "why")).toThrow(
      "Unable to optimize image and unable to fallback to upstream image",
    );
  });
});

// Divergence 4. Next applies no allowlist at all; CVE-2026-66066 (CVSS 9.5) is
// what an unfuzzed libvips loader on attacker bytes costs.
describe("the loader allowlist", () => {
  test("admits the three buffer loaders and nothing else", async () => {
    for (const format of ["jpeg", "png", "webp"] as const) {
      await expect(sharp(await solid(format)).metadata()).resolves.toBeTruthy();
    }
  });

  test("refuses a format outside it", async () => {
    // A valid single-frame GIF. libvips can read it; it is blocked anyway.
    const gif = new Uint8Array([
      0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00, 0x80, 0x00, 0x00,
      0xff, 0xff, 0xff, 0x00, 0x00, 0x00, 0x2c, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00,
      0x01, 0x00, 0x00, 0x02, 0x02, 0x44, 0x01, 0x00, 0x3b,
    ]);
    await expect(sharp(gif).metadata()).rejects.toThrow();
  });

  test("refuses an SVG buffer, which is what CVE-2023-38633 traversed", async () => {
    await expect(sharp(svg()).metadata()).rejects.toThrow();
  });
});
