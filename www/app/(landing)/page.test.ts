import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const www = join(import.meta.dirname, "../..");
const page = readFileSync(join(www, "app", "(landing)", "page.tsx"), "utf8");

describe("the landing page", () => {
  it("imports only entry points the ocel package exports", () => {
    const manifest = JSON.parse(
      readFileSync(join(www, "..", "packages", "ocel", "package.json"), "utf8"),
    ) as { exports: Record<string, unknown> };
    const imported = [...page.matchAll(/from "ocel\/([^"]+)"/g)].flatMap((m) =>
      m[1] ? [m[1]] : [],
    );
    expect(imported.length).toBeGreaterThanOrEqual(3);
    expect(imported.filter((path) => !(`./${path}` in manifest.exports))).toEqual([]);
  });

  it("lists only frameworks the docs have a page for", () => {
    const frameworks = [...page.matchAll(/\{ name: "([^"]+)", Logo: \w+ \}/g)].flatMap((m) =>
      m[1] ? [m[1]] : [],
    );
    expect(frameworks.length).toBeGreaterThanOrEqual(5);
    const undocumented = frameworks.filter((name) => {
      const slug = name.toLowerCase().replace(/[^a-z]/g, "");
      return !existsSync(join(www, "content", "docs", "frameworks", `${slug}.mdx`));
    });
    expect(undocumented).toEqual([]);
  });

  it("reruns whenever the ocel package's entry points change", () => {
    const turbo = JSON.parse(readFileSync(join(www, "turbo.json"), "utf8")) as {
      tasks: { test: { inputs: string[] } };
    };
    expect(turbo.tasks.test.inputs).toContain("$TURBO_ROOT$/packages/ocel/package.json");
  });
});
