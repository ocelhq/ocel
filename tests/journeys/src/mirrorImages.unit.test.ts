import { describe, expect, it } from "bun:test";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

const ROOTS = [import.meta.dir, join(import.meta.dir, "../../fronts")];
const MIRROR_IMAGE = /mirror\.gcr\.io\/[\w./-]+(?::([\w.-]+))?(@sha256:[0-9a-f]{64})?/g;
const VERSION_TAG = /^v?\d+\.\d+(\.\d+)*(-[a-z0-9.]+)?$/;

function listSourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return listSourceFiles(path);
    return /\.(ts|sh)$/.test(name) && !name.endsWith(".test.ts") ? [path] : [];
  });
}

describe("mirror images", () => {
  it("carry a major.minor version tag or a digest, never a floating tag", () => {
    const floating: string[] = [];
    for (const path of ROOTS.flatMap(listSourceFiles)) {
      for (const match of readFileSync(path, "utf8").matchAll(MIRROR_IMAGE)) {
        const tag = match[1];
        const pinned = match[2] !== undefined || (tag !== undefined && VERSION_TAG.test(tag));
        if (!pinned) {
          floating.push(`${path}: ${match[0]}`);
        }
      }
    }
    expect(floating).toEqual([]);
  });
});
