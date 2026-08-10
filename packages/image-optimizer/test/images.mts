import { sharp } from "../src/sharp.mjs";

export async function solid(
  format: "jpeg" | "png" | "webp",
  width = 200,
  height = 120,
): Promise<Uint8Array> {
  const pipeline = sharp({
    create: { width, height, channels: 3, background: { r: 200, g: 40, b: 60 } },
  });
  const buffer = await (format === "jpeg"
    ? pipeline.jpeg()
    : format === "png"
      ? pipeline.png()
      : pipeline.webp()
  ).toBuffer();
  return new Uint8Array(buffer);
}

export function bytes(...values: number[]): Uint8Array {
  return new Uint8Array(values);
}

export function animatedGif(): Uint8Array {
  const gce = [0x00, 0x21, 0xf9, 0x04, 0x04, 0x0a, 0x00, 0x00, 0x00];
  return new Uint8Array([
    0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x10, 0x00, 0x10, 0x00, 0x00, 0x00, 0x00,
    ...gce,
    0x2c, 0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x10, 0x00, 0x00,
    ...gce,
    0x3b,
  ]);
}

export function stillGif(): Uint8Array {
  return new Uint8Array([
    0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x10, 0x00, 0x10, 0x00, 0x00, 0x00, 0x00,
    0x2c, 0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x10, 0x00, 0x00, 0x3b,
  ]);
}

export function svg(): Uint8Array {
  return new TextEncoder().encode(
    '<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"></svg>',
  );
}

export function ico(): Uint8Array {
  return new Uint8Array([0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x10, 0x10, 0x00, 0x00]);
}
