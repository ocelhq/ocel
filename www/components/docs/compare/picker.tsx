"use client";

import type { ReactNode } from "react";
import { useEffect, useState } from "react";
import { Group, Panel, Separator } from "react-resizable-panels";
import { LABEL } from "./label";

export type RenderedPane = { owner: string; filename?: string; node: ReactNode };

export type RenderedRow = {
  key: string;
  question: string;
  verdict: ReactNode;
  table: ReactNode;
  them: RenderedPane;
  ocel: RenderedPane;
};

export type PickerFamily = {
  slug: string;
  name: string;
  lede: string;
  tools: { slug: string; name: string }[];
};

const SELECT =
  "compare-select w-full min-w-[14rem] appearance-none border border-(--hairline) bg-(--paper) py-[10px] pe-[40px] ps-[12px] font-display text-[1.0625rem] font-semibold text-(--ink) outline-none hover:border-(--steel) focus-visible:ring-2 focus-visible:ring-(--electric)/30 sm:w-auto";

function PaneBody({ pane }: { pane: RenderedPane }) {
  return (
    <>
      <div className="flex items-center justify-between gap-3 border-b border-(--hairline) px-4 py-3">
        <span className={`${LABEL} text-(--steel)`}>{pane.owner}</span>
        {pane.filename ? (
          <span className="truncate font-mono text-[0.75rem] leading-none text-(--steel)">
            {pane.filename}
          </span>
        ) : null}
      </div>
      <div className="compare-pane min-w-0 flex-1">{pane.node}</div>
    </>
  );
}

function useSplit() {
  const [split, setSplit] = useState(false);
  useEffect(() => {
    const query = window.matchMedia("(min-width: 48rem)");
    const sync = () => setSplit(query.matches);
    sync();
    query.addEventListener("change", sync);
    return () => query.removeEventListener("change", sync);
  }, []);
  return split;
}

function Panes({ row }: { row: RenderedRow }) {
  const split = useSplit();
  const code = Boolean(row.them.filename || row.ocel.filename);
  if (split && code) {
    return (
      <Group className="border border-(--hairline)">
        <Panel className="flex min-w-0 flex-col" defaultSize="50" minSize="20">
          <PaneBody pane={row.them} />
        </Panel>
        <Separator className="group/sep relative w-px shrink-0 bg-(--hairline) before:absolute before:inset-y-0 before:-start-1.5 before:w-3 hover:bg-(--steel) data-[separator=active]:bg-(--ink)">
          <span
            aria-hidden="true"
            className="absolute start-1/2 top-1/2 flex h-9 w-3.5 -translate-x-1/2 -translate-y-1/2 items-center justify-center gap-0.5 border border-(--hairline) bg-(--tile) group-hover/sep:border-(--steel) group-data-[separator=active]/sep:border-(--ink)"
          >
            <span className="h-4 w-px bg-(--steel) group-data-[separator=active]/sep:bg-(--ink)" />
            <span className="h-4 w-px bg-(--steel) group-data-[separator=active]/sep:bg-(--ink)" />
          </span>
        </Separator>
        <Panel className="flex min-w-0 flex-col" defaultSize="50" minSize="20">
          <PaneBody pane={row.ocel} />
        </Panel>
      </Group>
    );
  }
  return (
    <div className="grid border border-(--hairline) md:grid-cols-2">
      <div className="flex min-w-0 flex-col">
        <PaneBody pane={row.them} />
      </div>
      <div className="flex min-w-0 flex-col border-(--hairline) max-md:border-t md:border-s">
        <PaneBody pane={row.ocel} />
      </div>
    </div>
  );
}

export function Picker({
  rendered,
  families,
  names,
  initial,
}: {
  rendered: Record<string, RenderedRow[]>;
  families: PickerFamily[];
  names: Record<string, string>;
  initial: string;
}) {
  const [selected, setSelected] = useState(initial);

  useEffect(() => {
    const asked = new URLSearchParams(window.location.search).get("vs");
    if (asked !== null && asked !== initial) {
      window.history.replaceState(null, "", `?vs=${initial}`);
    }
  }, [initial]);

  function select(slug: string) {
    setSelected(slug);
    window.history.replaceState(null, "", `?vs=${slug}`);
  }

  const family = families.find((each) =>
    each.tools.some((tool) => tool.slug === selected),
  ) as PickerFamily;
  const pick = rendered[selected].find((row) => row.key === "pick") as RenderedRow;
  const rows = rendered[selected].filter((row) => row.key !== "pick");

  return (
    <div className="not-prose">
      <header className="border-b border-(--hairline) pb-10">
        <h1 className="text-balance font-display text-[2.5rem] font-semibold leading-[1.1] tracking-[-0.035em] text-(--ink)">
          Ocel vs <span className="text-(--electric)">{names[selected]}</span>
        </h1>
        <p className="mt-5 max-w-[34ch] text-lg leading-[1.55] text-(--body)">
          The same questions, asked of each tool. Ocel&apos;s answer is always your own account.
        </p>

        <div className="mt-8 flex flex-wrap gap-x-6 gap-y-4">
          <div className="flex w-full flex-col gap-2 sm:w-auto">
            <label htmlFor="compare-category" className={`${LABEL} text-(--steel)`}>
              Category
            </label>
            <select
              id="compare-category"
              className={SELECT}
              value={family.slug}
              onChange={(event) => {
                const next = families.find((each) => each.slug === event.target.value);
                if (next) select(next.tools[0].slug);
              }}
            >
              {families.map((each) => (
                <option key={each.slug} value={each.slug}>
                  {each.name}
                </option>
              ))}
            </select>
          </div>

          <div className="flex w-full flex-col gap-2 sm:w-auto">
            <label htmlFor="compare-tool" className={`${LABEL} text-(--steel)`}>
              Tool
            </label>
            <select
              id="compare-tool"
              className={SELECT}
              value={selected}
              onChange={(event) => select(event.target.value)}
            >
              {family.tools.map((tool) => (
                <option key={tool.slug} value={tool.slug}>
                  {tool.name}
                </option>
              ))}
            </select>
          </div>
        </div>
      </header>

      <section className="mt-10">
        <h2 className="mb-4 text-balance font-display text-[1.375rem] font-semibold leading-[1.25] tracking-[-0.03em] text-(--ink)">
          {pick.question}
        </h2>
        <Panes row={pick} />
      </section>

      {rows.map((row) => (
        <section key={row.key}>
          <h2 className="mt-14 mb-4 text-balance font-display text-[1.375rem] font-semibold leading-[1.25] tracking-[-0.03em] text-(--ink)">
            {row.question}
          </h2>
          {row.table ?? <Panes row={row} />}
          {row.verdict ? (
            <p className="mt-4 max-w-[65ch] text-base leading-[1.6] text-(--body)">{row.verdict}</p>
          ) : null}
        </section>
      ))}
    </div>
  );
}
