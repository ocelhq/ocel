export const SNIFF_WINDOW = 1024;

export const JPEG = "image/jpeg";
export const PNG = "image/png";
export const GIF = "image/gif";
export const WEBP = "image/webp";
export const AVIF = "image/avif";
export const SVG = "image/svg+xml";
export const ICO = "image/x-icon";
export const ICNS = "image/x-icns";
export const BMP = "image/bmp";
export const JXL = "image/jxl";
export const HEIC = "image/heic";
export const TIFF = "image/tiff";

export const ANIMATABLE_TYPES = [WEBP, PNG, GIF];

export const BYPASS_TYPES = [SVG, ICO, ICNS, BMP, JXL, HEIC];

type Signature = Array<number | null>;

function matches(bytes: Uint8Array, pattern: Signature): boolean {
  if (bytes.byteLength < pattern.length) return false;
  return pattern.every((b, i) => b === null || bytes[i] === b);
}

const ANY = null;

const SIGNATURES: Array<[Signature, string]> = [
  [[0xff, 0xd8, 0xff], JPEG],
  [[0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a], PNG],
  [[0x47, 0x49, 0x46, 0x38], GIF],
  [[0x52, 0x49, 0x46, 0x46, ANY, ANY, ANY, ANY, 0x57, 0x45, 0x42, 0x50], WEBP],
  [[0x3c, 0x3f, 0x78, 0x6d, 0x6c], SVG],
  [[0x3c, 0x73, 0x76, 0x67], SVG],
  [[ANY, ANY, ANY, ANY, 0x66, 0x74, 0x79, 0x70, 0x61, 0x76, 0x69, 0x66], AVIF],
  [[0x00, 0x00, 0x01, 0x00], ICO],
  [[0x69, 0x63, 0x6e, 0x73], ICNS],
  [[0x49, 0x49, 0x2a, 0x00], TIFF],
  [[0x42, 0x4d], BMP],
  [[0xff, 0x0a], JXL],
  [[0x00, 0x00, 0x00, 0x0c, 0x4a, 0x58, 0x4c, 0x20, 0x0d, 0x0a, 0x87, 0x0a], JXL],
  [[ANY, ANY, ANY, ANY, 0x66, 0x74, 0x79, 0x70, 0x68, 0x65, 0x69, 0x63], HEIC],
];

export function detectContentType(bytes: Uint8Array): string | null {
  if (bytes.byteLength === 0) return null;
  const head = bytes.subarray(0, SNIFF_WINDOW);
  for (const [pattern, type] of SIGNATURES) {
    if (matches(head, pattern)) return type;
  }
  return null;
}

const EXTENSIONS: Record<string, string> = {
  [JPEG]: "jpg",
  [PNG]: "png",
  [GIF]: "gif",
  [WEBP]: "webp",
  [AVIF]: "avif",
  [SVG]: "svg",
  [ICO]: "ico",
  [ICNS]: "icns",
  [BMP]: "bmp",
  [JXL]: "jxl",
  [HEIC]: "heic",
  [TIFF]: "tiff",
};

export function extensionFor(contentType: string): string | undefined {
  return EXTENSIONS[contentType];
}

export function isAnimated(bytes: Uint8Array, contentType: string): boolean {
  if (contentType === GIF) return isAnimatedGif(bytes);
  if (contentType === PNG) return isAnimatedPng(bytes);
  if (contentType === WEBP) return isAnimatedWebp(bytes);
  return false;
}

function isAnimatedGif(bytes: Uint8Array): boolean {
  let frames = 0;
  for (let i = 0; i < bytes.byteLength - 9; i++) {
    if (
      bytes[i] === 0x00 &&
      bytes[i + 1] === 0x21 &&
      bytes[i + 2] === 0xf9 &&
      bytes[i + 3] === 0x04
    ) {
      if (++frames > 1) return true;
    }
  }
  return false;
}

function isAnimatedPng(bytes: Uint8Array): boolean {
  const end = Math.min(bytes.byteLength, 4096);
  for (let i = 8; i < end - 4; i++) {
    if (
      bytes[i] === 0x61 &&
      bytes[i + 1] === 0x63 &&
      bytes[i + 2] === 0x54 &&
      bytes[i + 3] === 0x4c
    ) {
      return true;
    }
    if (
      bytes[i] === 0x49 &&
      bytes[i + 1] === 0x44 &&
      bytes[i + 2] === 0x41 &&
      bytes[i + 3] === 0x54
    ) {
      return false;
    }
  }
  return false;
}

function isAnimatedWebp(bytes: Uint8Array): boolean {
  const end = Math.min(bytes.byteLength, 4096);
  for (let i = 12; i < end - 4; i++) {
    if (
      bytes[i] === 0x41 &&
      bytes[i + 1] === 0x4e &&
      bytes[i + 2] === 0x49 &&
      bytes[i + 3] === 0x4d
    ) {
      return true;
    }
  }
  return false;
}
