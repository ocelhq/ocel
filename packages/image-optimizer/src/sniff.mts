// Content type from magic bytes, and from nothing else.
//
// The upstream's own Content-Type header is never consulted, not even as a
// fallback for bytes this table does not recognise. CVE-2025-55173 was exactly
// that fallback: a server that answers `Content-Type: image/png` for an HTML
// document gets its document served back under a type the browser will render,
// out of an origin the app owns. Bytes are the only thing an attacker cannot
// lie about here.
//
// Only the first 1024 bytes are ever examined. Every signature below fits in
// twelve, and the cap is what keeps this from becoming a scan over an
// attacker-sized buffer — as well as being what the design pins.

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

// Types that can hold an animation, and so must be checked for one before a
// transform flattens it to a single frame.
export const ANIMATABLE_TYPES = [WEBP, PNG, GIF];

// Types sharp either cannot usefully re-encode or must not be handed at all.
// SVG is here because it is markup, not pixels: it reaches this list only when
// dangerouslyAllowSVG is on, and then it is passed through byte-for-byte rather
// than parsed.
export const BYPASS_TYPES = [SVG, ICO, ICNS, BMP, JXL, HEIC];

// `null` — and only `null` — means "any byte here". Every number in a pattern is
// a literal, including the 0x00 bytes that are genuinely part of the ICO, TIFF
// and JXL-container signatures.
//
// The wildcard has to be a marker that is not a byte value. Spelling it as "the
// byte 0x00" collapsed ICO's four-byte `00 00 01 00` to a single
// `bytes[2] === 0x01` test, and ICO is a BYPASS_TYPE — so a Windows PE
// (`4d 5a 01 00`), a gzip stream (`1f 8b 01 00`) and a ZIP local header
// (`50 4b 01 02`) were all sniffed as image/x-icon and returned byte-for-byte
// under that type. That is passthrough rule 1 defeated, which is the whole of
// the CVE-2025-55173 guard.
type Signature = Array<number | null>;

function matches(bytes: Uint8Array, pattern: Signature): boolean {
  if (bytes.byteLength < pattern.length) return false;
  return pattern.every((b, i) => b === null || bytes[i] === b);
}

// Wildcards appear in exactly two places: the four-byte box length ahead of an
// ISO-BMFF brand (AVIF, HEIC) and the four-byte file size ahead of RIFF's
// "WEBP" form tag. Both are lengths, and it is the brand that identifies the
// format.
const ANY = null;

const SIGNATURES: Array<[Signature, string]> = [
  [[0xff, 0xd8, 0xff], JPEG],
  [[0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a], PNG],
  [[0x47, 0x49, 0x46, 0x38], GIF],
  [[0x52, 0x49, 0x46, 0x46, ANY, ANY, ANY, ANY, 0x57, 0x45, 0x42, 0x50], WEBP],
  // "<?xml" and "<svg" — the two openings Next accepts, and the reason a
  // document that merely starts with markup cannot be smuggled past as an
  // image: it has to actually be one of these.
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

// Next 16 falls back to a sharp metadata() probe for bytes this table misses.
// That fallback is not reproduced: it is an unbounded libvips parse of exactly
// the input this function has just failed to identify, run before any of the
// loader allowlist's protection would apply to it. An unrecognised payload is
// answered as not-an-image instead, which is what the fallback resolves to for
// every input that is not already a format sharp could have loaded anyway.
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

// Whether the bytes hold more than one frame. Only the three ANIMATABLE_TYPES
// reach this, and each is answered by walking its own container rather than by
// decoding anything — an animation check that decoded would be the very
// libvips parse the bypass exists to avoid.
export function isAnimated(bytes: Uint8Array, contentType: string): boolean {
  if (contentType === GIF) return isAnimatedGif(bytes);
  if (contentType === PNG) return isAnimatedPng(bytes);
  if (contentType === WEBP) return isAnimatedWebp(bytes);
  return false;
}

// More than one Graphics Control Extension / Image Descriptor pair. Walking the
// block structure is exact where a byte scan is not, but a full walk of an
// attacker-sized GIF is not worth it either: a GIF with two image descriptors
// is animated, and finding the second is enough to stop.
function isAnimatedGif(bytes: Uint8Array): boolean {
  let frames = 0;
  for (let i = 0; i < bytes.byteLength - 9; i++) {
    // 0x00 0x21 0xF9 0x04 — the terminator of the previous block followed by
    // the Graphics Control Extension introducer, which precedes every frame in
    // an animation and appears at most once in a still.
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

// An APNG declares itself with an acTL chunk, which the spec requires before
// the first IDAT. Scanning past IDAT would be scanning the pixel data.
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

// An animated WebP is a RIFF container whose fourth chunk tag is VP8X with the
// animation bit set; the ANMF chunks follow. The tag is enough.
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
