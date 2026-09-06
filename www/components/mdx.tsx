import { Callout } from "fumadocs-ui/components/callout";
import { Card, Cards } from "fumadocs-ui/components/card";
import { CodeBlock, Pre } from "fumadocs-ui/components/codeblock";
import { Tab, Tabs } from "fumadocs-ui/components/tabs";
import defaultMdxComponents from "fumadocs-ui/mdx";
import type { MDXComponents } from "mdx/types";
import type { ComponentProps } from "react";
import { DevTerminal } from "@/components/dev-terminal";
import { Hero } from "@/components/hero";
import { StartCards } from "@/components/start-cards";

function Tile({ className, ...props }: ComponentProps<typeof Card>) {
  return (
    <Card
      {...props}
      className={[
        "border-(--hairline) bg-(--fog) p-5 text-(--ink) shadow-none hover:border-(--steel) hover:bg-(--fog) [&_p]:text-(--body)",
        className,
      ]
        .filter(Boolean)
        .join(" ")}
    />
  );
}

function TileGrid({ className, ...props }: ComponentProps<typeof Cards>) {
  return (
    <Cards
      {...props}
      className={["grid-cols-[repeat(auto-fit,minmax(16rem,1fr))] gap-4", className]
        .filter(Boolean)
        .join(" ")}
    />
  );
}

function Compare(props: ComponentProps<"div">) {
  return (
    <div
      {...props}
      className="not-prose my-4 grid overflow-hidden border border-(--hairline) md:grid-cols-2 [&>figure]:my-0 [&>figure]:border-0 [&>figure]:shadow-none [&>figure+figure>div:first-child>*]:invisible"
    />
  );
}

function SquareCallout({ className, ...props }: ComponentProps<typeof Callout>) {
  return (
    <Callout
      {...props}
      className={[
        "border border-(--hairline) border-s-2 border-s-(--callout-color) ps-3 shadow-none [&>div[role=none]]:hidden",
        className,
      ]
        .filter(Boolean)
        .join(" ")}
    />
  );
}

export function getMDXComponents(components?: MDXComponents) {
  return {
    ...defaultMdxComponents,
    pre: ({ children, ...props }: ComponentProps<typeof CodeBlock>) => (
      <CodeBlock {...props} allowCopy={false}>
        <Pre>{children}</Pre>
      </CodeBlock>
    ),
    Callout: SquareCallout,
    Compare,
    Card: Tile,
    Cards: TileGrid,
    Hero,
    StartCards,
    DevTerminal,
    Tabs,
    Tab,
    ...components,
  } satisfies MDXComponents;
}

export const useMDXComponents = getMDXComponents;

declare global {
  type MDXProvidedComponents = ReturnType<typeof getMDXComponents>;
}
