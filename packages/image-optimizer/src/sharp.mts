// The one place sharp is imported, so that the hardening below is not
// something a future import can route around.
//
// Everything here happens in a fixed order and the order is the point:
// VIPS_BLOCK_UNTRUSTED is read by libvips when the library initialises, which
// happens inside sharp's native binding as it loads. Set it after the import
// and it has no effect at all. A static `import sharp from "sharp"` would be
// hoisted above these assignments by the module evaluator, so the import is
// deliberately dynamic and this module deliberately uses top-level await.

// libvips only ever runs loaders it considers fuzzed and safe for untrusted
// input. CVE-2026-66066 (CVSS 9.5, arbitrary file content disclosure through an
// unfuzzed operation) is what this answers, and Next sets it nowhere — there is
// nothing upstream to copy here.
process.env.VIPS_BLOCK_UNTRUSTED = "1";

// One libvips worker per transform. This function runs on 1769 MB and a single
// vCPU, and peak resident memory scales with the product of the two pools
// below, so both are pinned rather than left to a default that reads the host's
// core count — on Lambda that count is not the count we are billed for.
export const SHARP_CONCURRENCY = 1;

// The libuv pool sharp's own toBuffer work, DNS lookups and file reads all
// share. Left at libuv's default value explicitly: raising it multiplies peak
// memory against the line above, and lowering it to 1 would let a slow DNS
// lookup block the very read it is waiting on.
process.env.UV_THREADPOOL_SIZE ??= "4";

const { default: sharp } = await import("sharp");

// Deny every image loader, then re-admit three. Next ships no allowlist at all;
// this is a deliberate divergence, and it is what stands between a malformed
// file and every parser libvips can dispatch to — GIF, TIFF, PDF, SVG, OpenEXR,
// the ImageMagick bridge — of which only a handful have ever been fuzzed.
//
// The *Buffer suffix matters as much as the format list: the file-input loaders
// are what CVE-2023-38633 traversed (an SVG that names a local path libvips
// then reads), and a buffer-only allowlist removes that shape structurally
// rather than by validating a path.
//
// The cost is that a still GIF and an AVIF source now fail to load. That is not
// a hole, it is a degradation: the failure lands on the passthrough path and
// the original bytes are served unmodified, which is already what an animated
// GIF gets.
sharp.block({ operation: ["VipsForeignLoad"] });
sharp.unblock({
  operation: [
    "VipsForeignLoadJpegBuffer",
    "VipsForeignLoadPngBuffer",
    "VipsForeignLoadWebpBuffer",
  ],
});

// Every input here is unique and attacker-named, so the operation cache's hit
// rate is zero by construction and all it does is hold decoded pixel buffers
// and file descriptors alive against a memory limit an attacker chose the shape
// of.
sharp.cache(false);
sharp.concurrency(SHARP_CONCURRENCY);

export { sharp };
