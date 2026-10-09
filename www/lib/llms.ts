import type * as PageTree from "fumadocs-core/page-tree";

export function markdownUrl(url: string): string {
  return url === "/docs" ? "/docs/index.md" : `${url}.md`;
}

export function pageSlugs(slug: string[] | undefined): string[] {
  const slugs = slug?.slice(0, -1) ?? [];
  return slugs.length === 1 && slugs[0] === "index" ? [] : slugs;
}

const codeAnnotation =
  /[ \t]*(?:\/\/|#|\{\/\*|\/\*|<!--)[ \t]*\[!code [^\]\n]*\][ \t]*(?:\*\/\}|\*\/|-->)?[ \t]*$/gm;

export function withoutCodeAnnotations(markdown: string): string {
  return markdown.replace(codeAnnotation, "");
}

type Entry = { section: string; line: string };

const text = (node: unknown) => (typeof node === "string" ? node.trim() : "");

function link(item: PageTree.Item): string {
  const entry = `- [${text(item.name)}](${markdownUrl(item.url)})`;
  const description = text(item.description);
  return description ? `${entry}: ${description}` : entry;
}

function collect(nodes: PageTree.Node[], section: string, folder: string, entries: Entry[]) {
  let current = section;
  for (const node of nodes) {
    if (node.type === "page") {
      entries.push({ section: current, line: link(node) });
    } else if (node.type === "separator") {
      current = folder ? `${folder}: ${text(node.name)}` : text(node.name);
    } else {
      const name = text(node.name);
      const inner = node.root ? name : current;
      if (node.index) entries.push({ section: inner, line: link(node.index) });
      collect(node.children, inner, node.root || !folder ? name : `${folder}: ${name}`, entries);
    }
  }
}

export function llmsIndex(tree: PageTree.Root, site: { title: string; summary: string }): string {
  const entries: Entry[] = [];
  collect(tree.children, text(tree.name), "", entries);
  const sections: { name: string; lines: string[] }[] = [];
  for (const { section, line } of entries) {
    const last = sections.at(-1);
    if (last?.name === section) last.lines.push(line);
    else sections.push({ name: section, lines: [line] });
  }
  const body = sections.map((s) => `## ${s.name}\n\n${s.lines.join("\n")}\n`);
  return [`# ${site.title}\n`, `> ${site.summary}\n`, ...body].join("\n");
}
