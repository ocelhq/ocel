import {
  ArrowDownTrayIcon,
  BoltIcon,
  BookOpenIcon,
  CommandLineIcon,
  RocketLaunchIcon,
  ScaleIcon,
  Squares2X2Icon,
} from "@heroicons/react/24/outline";
import type * as PageTree from "fumadocs-core/page-tree";
import { type InferPageType, loader } from "fumadocs-core/source";
import { defineDocs } from "fumadocs-mdx/macro";
import { createElement } from "react";
import { withoutCodeAnnotations } from "./llms";

const docs = defineDocs({
  dir: "content/docs",
  docs: {
    postprocess: {
      includeProcessedMarkdown: {
        headingIds: false,
        stringify(node) {
          if (node.type === "mdxJsxFlowElement" || node.type === "mdxJsxTextElement") {
            node.data = { ...node.data, _stringify: "children-only" };
          }
          return undefined;
        },
      },
    },
  },
});

const icons = {
  ArrowDownTrayIcon,
  BoltIcon,
  BookOpenIcon,
  CommandLineIcon,
  Squares2X2Icon,
  ScaleIcon,
};

export const source = loader({
  baseUrl: "/docs",
  source: docs.toFumadocsSource(),
  icon(name) {
    if (name && name in icons) return createElement(icons[name as keyof typeof icons]);
  },
});

export type DocsPage = InferPageType<typeof source>;

export async function pageMarkdown(page: DocsPage): Promise<string> {
  const heading = page.data.description
    ? `# ${page.data.title}\n\n> ${page.data.description}`
    : `# ${page.data.title}`;
  const body = withoutCodeAnnotations(await page.data.getText("processed")).trim();
  return `${heading}\n\n${body}\n`;
}

export const tabColors: Record<string, string> = {
  guide: "var(--electric)",
  cli: "var(--amber)",
  sdk: "var(--violet)",
};

export function layoutTree(): PageTree.Root {
  const tree = source.getPageTree();
  const roots = tree.children.filter((n) => n.type === "folder" && n.root);
  const rest = tree.children.filter((n) => !roots.includes(n));
  const index = rest.find((n) => n.type === "page" && n.url === "/docs");
  const guide: PageTree.Folder = {
    type: "folder",
    $id: "guide",
    name: "Deploy",
    description: "Your apps on your infra.",
    icon: createElement(RocketLaunchIcon),
    root: true,
    index: index?.type === "page" ? index : undefined,
    children: rest,
  };
  return { ...tree, children: [guide, ...roots] };
}
