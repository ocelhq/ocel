import { describe, expect, it } from "bun:test";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

const ROOTS = [import.meta.dir, join(import.meta.dir, "../../fronts")];
const FLOATING = new Set(["stable", "latest", "mainline", "stable-alpine"]);
const MIRROR_IMAGE = /mirror\.gcr\.io\/[\w./-]+(?::([\w.-]+))?/g;

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return files(path);
    return /\.(ts|sh)$/.test(name) && !name.endsWith(".test.ts") ? [path] : [];
  });
}

describe("mirror images", () => {
  it("run a pinned version tag, never a floating one", () => {
    const floating: string[] = [];
    for (const path of ROOTS.flatMap(files)) {
      for (const match of readFileSync(path, "utf8").matchAll(MIRROR_IMAGE)) {
        const tag = match[1];
        if (tag === undefined || FLOATING.has(tag)) {
          floating.push(`${path}: ${match[0]}`);
        }
      }
    }
    expect(floating).toEqual([]);
  });
});
