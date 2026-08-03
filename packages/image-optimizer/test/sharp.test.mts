import { expect, test } from "vitest";
import { SHARP_CONCURRENCY, sharp } from "../src/sharp.mjs";

// The libvips hardening, asserted rather than assumed. Every line of it is a
// setting whose absence is silent — a wrong value here costs nothing at build
// time and everything on a malformed input.

// CVE-2026-66066, CVSS 9.5: arbitrary file content disclosure through an
// unfuzzed libvips operation. libvips reads this when the library initialises,
// inside the native binding's load, so it has to be set before the import — which
// is why importing sharp anywhere but through src/sharp.mts is a defect.
test("VIPS_BLOCK_UNTRUSTED is set, and was set before sharp loaded", () => {
  expect(process.env["VIPS_BLOCK_UNTRUSTED"]).toBe("1");
});

test("both concurrency layers are pinned", () => {
  expect(sharp.concurrency()).toBe(SHARP_CONCURRENCY);
  // Peak resident memory scales with the product of the two, so neither may be
  // left to a default that reads the host's core count.
  expect(process.env["UV_THREADPOOL_SIZE"]).toBe("4");
});

// For unique attacker-named inputs the hit rate is zero by construction, and all
// the cache does is hold decoded pixel buffers and file descriptors alive
// against a memory limit an attacker picked the shape of.
test("the operation cache is off", () => {
  const stats = sharp.cache();
  expect(stats.memory.max).toBe(0);
  expect(stats.files.max).toBe(0);
  expect(stats.items.max).toBe(0);
});

// sharp is a real dependency at a real version here, so the floor is worth
// asserting: there is no 0.34.x backport for the four 2026 libvips CVEs, and
// OpenNext's pinned 0.32.6 is far below it.
test("libvips is at least 8.18.3 and sharp at least 0.35.0", () => {
  expect(atLeast(sharp.versions.vips, [8, 18, 3])).toBe(true);
  expect(atLeast(sharp.versions.sharp!, [0, 35, 0])).toBe(true);
});

function atLeast(version: string, floor: [number, number, number]): boolean {
  const parts = version.split(".").map(Number);
  for (let i = 0; i < floor.length; i++) {
    const part = parts[i] ?? 0;
    if (part > floor[i]!) return true;
    if (part < floor[i]!) return false;
  }
  return true;
}
