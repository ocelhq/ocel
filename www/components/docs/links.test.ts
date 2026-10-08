import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const root = join(import.meta.dirname, "../..");

function listSources(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    if (entry.name === "node_modules" || entry.name.startsWith(".")) return [];
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return listSources(path);
    return entry.name.endsWith(".tsx") ? [path] : [];
  });
}

function findDocsHrefs(source: string): string[] {
  return [...source.matchAll(/href(?:=|: )"(\/docs[^"?#]*)/g)].flatMap((m) => (m[1] ? [m[1]] : []));
}

function isServed(href: string): boolean {
  const page = join(root, "content", "docs", href.slice("/docs".length));
  return existsSync(`${page}.mdx`) || existsSync(join(page, "index.mdx"));
}

describe("a link into the docs", () => {
  it("names a page the docs serve", () => {
    const dead = ["app", "components"]
      .flatMap((dir) => listSources(join(root, dir)))
      .flatMap((file) =>
        findDocsHrefs(readFileSync(file, "utf8"))
          .filter((href) => !isServed(href))
          .map((href) => `${file.slice(root.length + 1)}: ${href}`),
      );
    expect(dead).toEqual([]);
  });
});
