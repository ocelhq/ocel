import type * as PageTree from "fumadocs-core/page-tree";
import { describe, expect, it } from "vitest";
import { llmsIndex, markdownUrl, pageSlugs, withoutCodeAnnotations } from "./llms";

const page = (name: string, url: string, description?: string): PageTree.Item => ({
  type: "page",
  name,
  url,
  description,
});

const tree: PageTree.Root = {
  name: "Deploy",
  children: [
    page("Introduction", "/docs", "Ocel deploys apps to your own cloud."),
    page("Install", "/docs/install"),
    { type: "separator", name: "Deploy" },
    page("Configuration", "/docs/configuration"),
    { type: "separator", name: "Reference" },
    {
      type: "folder",
      name: "Errors",
      index: page("Errors", "/docs/errors", "Every error code."),
      children: [
        { type: "separator", name: "Project" },
        page("project.no_config", "/docs/errors/project.no_config", "No config file."),
      ],
    },
    {
      type: "folder",
      name: "Ocel CLI",
      root: true,
      index: page("CLI", "/docs/cli", "The ocel binary."),
      children: [
        { type: "separator", name: "Project" },
        page("deploy", "/docs/cli/deploy", "Ship it."),
      ],
    },
    { type: "separator", name: "Elsewhere" },
    page("Telemetry", "/docs/telemetry"),
  ],
};

describe("the markdown url of a docs page", () => {
  it("is the page url with .md appended", () => {
    expect(markdownUrl("/docs/errors/project.no_config")).toBe("/docs/errors/project.no_config.md");
  });

  it("is index.md for the docs root", () => {
    expect(markdownUrl("/docs")).toBe("/docs/index.md");
  });
});

describe("the slugs a markdown route serves", () => {
  it("drop the content.md segment the rewrite appends", () => {
    expect(pageSlugs(["errors", "project.no_config", "content.md"])).toEqual([
      "errors",
      "project.no_config",
    ]);
  });

  it("name the docs root for index.md", () => {
    expect(pageSlugs(["index", "content.md"])).toEqual([]);
  });

  it("name the docs root when there are none", () => {
    expect(pageSlugs(undefined)).toEqual([]);
  });
});

describe("llms.txt", () => {
  const index = llmsIndex(tree, { title: "Ocel", summary: "Ocel deploys apps to your own cloud." });

  it("opens with the title and the summary", () => {
    expect(index.startsWith("# Ocel\n\n> Ocel deploys apps to your own cloud.\n")).toBe(true);
  });

  it("links every page to its markdown with its description", () => {
    expect(index).toContain(
      "- [Introduction](/docs/index.md): Ocel deploys apps to your own cloud.",
    );
    expect(index).toContain("- [Install](/docs/install.md)\n");
    expect(index).toContain("- [Errors](/docs/errors.md): Every error code.");
    expect(index).toContain(
      "- [project.no_config](/docs/errors/project.no_config.md): No config file.",
    );
    expect(index).toContain("- [CLI](/docs/cli.md): The ocel binary.");
    expect(index).toContain("- [deploy](/docs/cli/deploy.md): Ship it.");
  });

  const section = (heading: string) => index.split(`## ${heading}\n\n`)[1]?.split("\n\n")[0];

  it("groups pages under a section for each separator and each root folder", () => {
    const headings = [...index.matchAll(/^## (.+)$/gm)].map((m) => m[1]);
    expect(headings).toEqual([
      "Deploy",
      "Reference",
      "Errors: Project",
      "Ocel CLI",
      "Ocel CLI: Project",
      "Elsewhere",
    ]);
  });

  it("keeps a separator named like the section before it in that section", () => {
    expect(section("Deploy")).toContain("[Install]");
    expect(section("Deploy")).toContain("[Configuration]");
  });

  it("lists a folder's index in the section that holds the folder", () => {
    expect(section("Reference")).toContain("[Errors]");
    expect(section("Ocel CLI")).toContain("[CLI]");
    expect(section("Ocel CLI: Project")).toContain("[deploy]");
  });

  it("returns to the outer section after a folder", () => {
    expect(section("Elsewhere")).toContain("[Telemetry]");
  });
});

describe("the markdown of a code block", () => {
  it("keeps a line marked as removed, without its marker", () => {
    expect(withoutCodeAnnotations("  provider: awsProvider(), // [!code --]\n")).toBe(
      "  provider: awsProvider(),\n",
    );
  });

  it("keeps a line marked as added, without its marker", () => {
    expect(withoutCodeAnnotations('  "provider": "aws", // [!code ++]')).toBe(
      '  "provider": "aws",',
    );
  });

  it("drops the marker in every comment style", () => {
    expect(
      withoutCodeAnnotations(
        ["a = 1 # [!code highlight]", "<b /> {/* [!code focus] */}", "c <!-- [!code --] -->"].join(
          "\n",
        ),
      ),
    ).toBe(["a = 1", "<b />", "c"].join("\n"));
  });

  it("leaves prose that only mentions a marker alone", () => {
    expect(withoutCodeAnnotations("Write `// [!code ++]` after a line.")).toBe(
      "Write `// [!code ++]` after a line.",
    );
  });
});
