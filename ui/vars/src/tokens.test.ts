import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("./tokens.css", import.meta.url), "utf8");

function block(selector: string): string {
  const at = css.indexOf(selector);
  expect(at, `${selector} block`).toBeGreaterThan(-1);
  return css.slice(at, css.indexOf("\n}", at));
}

function token(selector: string, name: string): string {
  const found = new RegExp(`--${name}:\\s*([^;]+);`).exec(block(selector));
  expect(found, `--${name} in ${selector}`).not.toBeNull();
  return found![1]!.trim();
}

describe.each([":root", ".dark"])("%s surfaces", (selector) => {
  it("steps muted off card, so a hover or a selected row is visible on a card", () => {
    expect(token(selector, "muted")).not.toBe(token(selector, "card"));
  });

  it("steps muted off background", () => {
    expect(token(selector, "muted")).not.toBe(token(selector, "background"));
  });
});
