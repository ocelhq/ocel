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

function avifConfig() {
  return imageConfig({ formats: [AVIF, WEBP] });
}

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

  test("rejects a non-image before any bypass branch can return it", async () => {
    await expect(transform(input(new TextEncoder().encode("%PDF-1.7\n%%EOF")))).rejects.toThrow(
      "The requested resource isn't a valid image.",
    );
  });

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

  async function avifCall(overrides: Record<string, unknown>) {
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
      return { source, avif, pipeline, sharpCalls: [...spy.mock.calls] };
    } finally {
      spy.mockRestore();
    }
  }

  test("AVIF quality is rescaled and effort pinned", async () => {
    const { source, avif, pipeline, sharpCalls } = await avifCall({ quality: 75 });
    expect(avif).toHaveBeenCalledWith({ quality: 55, effort: 3 });
    expect(sharpCalls).toEqual([[source, { limitInputPixels: 268402689 }]]);
    expect(pipeline.timeout).toHaveBeenCalledWith({ seconds: 7 });
    expect(pipeline.resize).toHaveBeenCalledWith(100, undefined, {
      withoutEnlargement: true,
    });
    expect(pipeline.rotate.mock.invocationCallOrder[0]!).toBeLessThan(
      pipeline.resize.mock.invocationCallOrder[0]!,
    );
  });

  test("the AVIF rescale floors at 1, never at 0 or below", async () => {
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

describe("the loader allowlist", () => {
  test("admits the three buffer loaders and nothing else", async () => {
    for (const format of ["jpeg", "png", "webp"] as const) {
      await expect(sharp(await solid(format)).metadata()).resolves.toBeTruthy();
    }
  });

  test("refuses a format outside it", async () => {
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
