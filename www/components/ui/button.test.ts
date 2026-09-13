import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const root = join(import.meta.dirname, "../..");

function sources(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    if (entry.name === "node_modules" || entry.name.startsWith(".")) return [];
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return sources(path);
    return entry.name.endsWith(".tsx") ? [path] : [];
  });
}

function buttonsRenderingAnotherElement(source: string): string[] {
  return [...source.matchAll(/<Button\b[^>]*?render=\{<(?!button\b)[^>]*>/g)].map((m) => m[0]);
}

describe("Button", () => {
  it("drops native button semantics whenever it renders a non-button element", () => {
    const offenders = ["app", "components"]
      .flatMap((dir) => sources(join(root, dir)))
      .flatMap((file) =>
        buttonsRenderingAnotherElement(readFileSync(file, "utf8"))
          .filter((tag) => !tag.includes("nativeButton={false}"))
          .map((tag) => `${file.slice(root.length + 1)}: ${tag.replace(/\s+/g, " ").slice(0, 80)}`),
      );
    expect(offenders).toEqual([]);
  });
});
