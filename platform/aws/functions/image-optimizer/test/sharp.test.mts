import { expect, test } from "vitest";
import { SHARP_CONCURRENCY, sharp } from "../src/sharp.mjs";

test("VIPS_BLOCK_UNTRUSTED is set, and was set before sharp loaded", () => {
  expect(process.env["VIPS_BLOCK_UNTRUSTED"]).toBe("1");
});

test("both concurrency layers are pinned", () => {
  expect(sharp.concurrency()).toBe(SHARP_CONCURRENCY);
  expect(process.env["UV_THREADPOOL_SIZE"]).toBe("4");
});

test("the operation cache is off", () => {
  const stats = sharp.cache();
  expect(stats.memory.max).toBe(0);
  expect(stats.files.max).toBe(0);
  expect(stats.items.max).toBe(0);
});

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
