import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const www = join(import.meta.dirname, "../..");
const read = (...path: string[]) => readFileSync(join(www, ...path), "utf8");
const page = read("app", "(landing)", "page.tsx");
const primitives = read("components", "landing", "primitives.ts");
const diagram = read("components", "landing", "deploy-diagram.tsx");
const targets = read("components", "landing", "targets.ts");

describe("the landing page", () => {
  it("imports only entry points the ocel package exports", () => {
    const manifest = JSON.parse(read("..", "packages", "ocel", "package.json")) as {
      exports: Record<string, unknown>;
    };
    const imported = [
      ...`${page}\n${primitives}\n${targets}`.matchAll(/from "ocel\/([^"]+)"/g),
    ].flatMap((m) => (m[1] ? [m[1]] : []));
    expect(imported.length).toBeGreaterThanOrEqual(3);
    expect(imported.filter((path) => !(`./${path}` in manifest.exports))).toEqual([]);
  });

  it("draws only frameworks the docs have a page for", () => {
    const pages = [
      ...diagram.matchAll(/label: "[^"]+", Logo: \w+, ratio: [^,]+, docs: "([^"]+)"/g),
    ].flatMap((m) => (m[1] ? [m[1]] : []));
    expect(pages.length).toBeGreaterThanOrEqual(4);
    const undocumented = pages.filter(
      (slug) => !existsSync(join(www, "content", "docs", "frameworks", `${slug}.mdx`)),
    );
    expect(undocumented).toEqual([]);
  });

  it("links each resource tile to an SDK page that exists", () => {
    const docs = [...primitives.matchAll(/docs: "\/docs\/sdk\/([^"]+)"/g)].flatMap((m) =>
      m[1] ? [m[1]] : [],
    );
    expect(docs.length).toBeGreaterThanOrEqual(6);
    expect(
      docs.filter((slug) => !existsSync(join(www, "content", "docs", "sdk", `${slug}.mdx`))),
    ).toEqual([]);
  });

  it("reruns whenever the ocel package's entry points change", () => {
    const turbo = JSON.parse(read("turbo.json")) as { tasks: { test: { inputs: string[] } } };
    expect(turbo.tasks.test.inputs).toContain("$TURBO_ROOT$/packages/ocel/package.json");
  });
});
