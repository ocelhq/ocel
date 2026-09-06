import {
  BoltIcon,
  BookOpenIcon,
  CommandLineIcon,
  RocketLaunchIcon,
  ScaleIcon,
  Squares2X2Icon,
} from "@heroicons/react/24/outline";
import type * as PageTree from "fumadocs-core/page-tree";
import { loader } from "fumadocs-core/source";
import { defineDocs } from "fumadocs-mdx/macro";
import { createElement } from "react";

const docs = defineDocs({
  dir: "content/docs",
});

const icons = { BoltIcon, BookOpenIcon, CommandLineIcon, Squares2X2Icon, ScaleIcon };

export const source = loader({
  baseUrl: "/docs",
  source: docs.toFumadocsSource(),
  icon(name) {
    if (name && name in icons) return createElement(icons[name as keyof typeof icons]);
  },
});

export const tabColors: Record<string, string> = {
  guide: "var(--electric)",
  cli: "var(--color-amber-500)",
  sdk: "var(--color-violet-500)",
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
