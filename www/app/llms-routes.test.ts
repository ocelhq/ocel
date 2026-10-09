import { describe, expect, it } from "vitest";
import { GET as pageText } from "./llms.mdx/docs/[[...slug]]/route";
import { GET as fullText } from "./llms-full.txt/route";

const page = (slug: string[]) =>
  pageText(new Request("https://ocel.dev/llms.mdx/docs"), {
    params: Promise.resolve({ slug: [...slug, "content.md"] }),
  });

describe("the docs as markdown", () => {
  it("serves a page's code blocks without their annotation markers", async () => {
    const markdown = await (await page(["index"])).text();
    expect(markdown).toContain("provider: awsProvider(),");
    expect(markdown).not.toContain("[!code");
  });

  it("serves every page's code blocks without their annotation markers in the full text", async () => {
    const markdown = await (await fullText()).text();
    expect(markdown).toContain("provider: awsProvider(),");
    expect(markdown).not.toContain("[!code");
  });
});
