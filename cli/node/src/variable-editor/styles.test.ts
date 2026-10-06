import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import tailwind from "@tailwindcss/postcss";
import postcss from "postcss";
import { describe, expect, it } from "vitest";

const sheet = fileURLToPath(new URL("./styles.css", import.meta.url));

async function compiled(): Promise<string> {
  const result = await postcss([tailwind()]).process(await readFile(sheet, "utf8"), {
    from: sheet,
  });
  return result.css;
}

describe("the variable editor stylesheet", () => {
  it("styles the classes the published variables table uses, its open animations included", async () => {
    const css = await compiled();

    expect(css).toContain(".min-w-2xl");
    expect(css).toContain(".data-open\\:animate-in");
  });
});
