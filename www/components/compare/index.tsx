import { highlight } from "fumadocs-core/highlight";
import { CodeBlock, Pre } from "fumadocs-ui/components/codeblock";
import { Fragment, type ReactNode } from "react";
import type { Facts, Pane } from "./data";
import { families, find, landscape, tools } from "./data";
import { LABEL } from "./label";
import type { PickerFamily, RenderedPane, RenderedRow } from "./picker";
import { Picker } from "./picker";

export function inline(text: string): ReactNode {
  let offset = 0;
  return text.split("`").map((part, index) => {
    const key = String(offset);
    offset += part.length + 1;
    return index % 2 === 1 ? (
      <code
        key={key}
        className="bg-(--fog) px-[0.4em] py-[0.1em] font-mono text-[0.85em] text-(--ink)"
      >
        {part}
      </code>
    ) : (
      <span key={key}>{part}</span>
    );
  });
}

function FactTable({ them, ocel, owner }: { them: Facts; ocel: Facts; owner: string }) {
  return (
    <div className="grid grid-cols-2 border border-(--hairline) md:grid-cols-[auto_1fr_1fr] [&>*:nth-last-child(-n+3)]:border-b-0">
      <div className="max-md:hidden border-b border-(--hairline) px-4 py-3" />
      <div className="border-b border-(--hairline) px-4 py-3 md:border-s">
        <span className={`${LABEL} text-(--steel)`}>{owner}</span>
      </div>
      <div className="border-b border-(--hairline) px-4 py-3 border-s">
        <span className={`${LABEL} text-(--steel)`}>Ocel</span>
      </div>
      {them.map((fact, index) => (
        <Fragment key={fact.label}>
          <div
            className={`${LABEL} border-b border-(--hairline) px-4 py-3 text-(--steel) max-md:col-span-2 max-md:border-b-0 max-md:pt-4 max-md:pb-1 md:pt-4`}
          >
            {fact.label}
          </div>
          <div className="border-b border-(--hairline) px-4 py-3 text-[0.9375rem] leading-[1.6] text-(--body) max-md:pt-1 md:border-s">
            {inline(fact.value)}
          </div>
          <div className="border-b border-(--hairline) px-4 py-3 border-s text-[0.9375rem] leading-[1.6] text-(--body) max-md:pt-1">
            {inline(ocel[index].value)}
          </div>
        </Fragment>
      ))}
    </div>
  );
}

function Bullets({ items }: { items: string[] }) {
  return (
    <ul className="flex flex-col gap-3 p-4">
      {items.map((item) => (
        <li key={item} className="relative ps-4 text-[0.9375rem] leading-[1.6] text-(--body)">
          <span
            aria-hidden="true"
            className="absolute start-0 top-[0.72em] h-px w-2.5 bg-(--steel)"
          />
          {inline(item)}
        </li>
      ))}
    </ul>
  );
}

async function renderPane(pane: Pane, owner: string): Promise<RenderedPane> {
  if (pane.code) {
    const node = await highlight(pane.code.code, {
      lang: pane.code.lang,
      themes: { light: "github-light", dark: "github-dark" },
      defaultColor: false,
      components: { pre: Pre },
    });
    return {
      owner,
      filename: pane.code.filename,
      node: (
        <CodeBlock allowCopy={false} title={pane.code.filename}>
          {node}
        </CodeBlock>
      ),
    };
  }
  return { owner, node: <Bullets items={pane.list ?? []} /> };
}

export async function CompareWith({ initial }: { initial?: string }) {
  const entries = await Promise.all(
    tools.map(async (tool) => {
      const rows: RenderedRow[] = await Promise.all(
        tool.rows.map(async (row) => ({
          key: row.key,
          question: row.question,
          verdict: row.verdict ? inline(row.verdict) : null,
          table:
            row.them.facts && row.ocel.facts ? (
              <FactTable them={row.them.facts} ocel={row.ocel.facts} owner={tool.name} />
            ) : null,
          them: await renderPane(
            row.them,
            row.key === "pick" ? `Pick ${tool.name} when` : tool.name,
          ),
          ocel: await renderPane(row.ocel, row.key === "pick" ? "Pick Ocel when" : "Ocel"),
        })),
      );
      return [tool.slug, rows] as const;
    }),
  );

  const categories: PickerFamily[] = families.map((family) => ({
    slug: family.slug,
    name: family.name,
    lede: family.lede,
    tools: tools
      .filter((tool) => tool.family === family.slug)
      .map((tool) => ({ slug: tool.slug, name: tool.name })),
  }));

  const names = Object.fromEntries(tools.map((tool) => [tool.slug, tool.name]));

  return (
    <main className="w-full">
      <Picker
        rendered={Object.fromEntries(entries)}
        families={categories}
        names={names}
        landscape={landscape()}
        initial={find(initial).slug}
      />
    </main>
  );
}
