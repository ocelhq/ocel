import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("./tokens.css", import.meta.url), "utf8");

function block(selector: string): string {
  const exact = css.indexOf(`\n\n${selector} {`);
  const at = exact > -1 ? exact : css.indexOf(`\n\n${selector},`);
  expect(at, `${selector} block`).toBeGreaterThan(-1);
  return css.slice(at, css.indexOf("\n}", at + 1));
}

function token(selector: string, name: string): string | undefined {
  return new RegExp(`--${name}:\\s*([^;]+);`).exec(block(selector))?.[1]?.trim();
}

describe.each([":root", ".dark"])("%s surfaces", (selector) => {
  const resolved = (name: string) => token(selector, name) ?? token(":root", name);

  it("steps muted off card, so a hover or a selected row is visible on a card", () => {
    expect(resolved("muted")).not.toBe(resolved("card"));
  });

  it("steps muted off background", () => {
    expect(resolved("muted")).not.toBe(resolved("background"));
  });
});

describe("the one annotation", () => {
  it("fills a primary action with ink, never electric", () => {
    expect(token(":root,\n.dark", "primary")).toBe("var(--foreground)");
    expect(token(":root", "primary")).toBeUndefined();
    expect(token(".dark", "primary")).toBeUndefined();
  });

  it("keeps electric for focus", () => {
    expect(token(":root,\n.dark", "ring")).toBe("var(--electric)");
  });

  it("resolves every alias where dark applies, so a dark subtree never inherits light ink", () => {
    for (const selector of [":root", ".dark"]) {
      expect(block(selector), selector).not.toMatch(/var\(/);
    }
  });
});

describe("register dials", () => {
  it("floats without blur in every register", () => {
    for (const selector of [
      ":root,\n.dark",
      '[data-register="landing"]',
      '[data-register="read"]',
    ]) {
      const shadow = token(selector, "float-shadow");
      if (shadow === undefined || shadow === "none") continue;
      expect(shadow, selector).toMatch(/^\d+px \d+px 0 0 /);
    }
  });

  it("gives landing the full print offset and read none", () => {
    expect(token('[data-register="landing"]', "float-shadow")).toMatch(/^6px 6px/);
    expect(token('[data-register="read"]', "float-shadow")).toBe("none");
  });
});
