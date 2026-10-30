"use client";

import {
  Archive,
  Broadcast,
  Database,
  type Icon,
  Key,
  Megaphone,
  Timer,
} from "@phosphor-icons/react";
import { useEffect, useState } from "react";
import { type Language, languages, type PrimitiveName } from "@/components/landing/primitives";
import { cn } from "@/lib/utils";

type Pane = { file: string; html: string };

export type RenderedPrimitive = {
  name: PrimitiveName;
  what: string;
  docs: string;
  /** Highlighted html per language, rendered on the server: where it's declared, where it's used. */
  code: Record<Language, { declare: Pane; use: Pane }>;
};

const icons: Record<PrimitiveName, Icon> = {
  postgres: Database,
  bucket: Archive,
  kv: Key,
  task: Timer,
  topic: Megaphone,
  realtime: Broadcast,
};

const storageKey = "ocel.landing.language";

function CodePane({ pane }: { pane: Pane }) {
  return (
    <div className="flex min-w-0 flex-col">
      <div className="truncate border-b border-terminal-rule px-4 py-2 font-mono text-[11px] text-terminal-foreground">
        {pane.file}
      </div>
      <div
        className="shiki-pane px-4 py-3.5 [&_.shiki]:text-[12.5px]"
        dangerouslySetInnerHTML={{ __html: pane.html }}
      />
    </div>
  );
}

export function PrimitivesGrid({ primitives }: { primitives: RenderedPrimitive[] }) {
  const [language, setLanguage] = useState<Language>("TypeScript");

  useEffect(() => {
    const stored = localStorage.getItem(storageKey);
    if (stored && (languages as readonly string[]).includes(stored)) {
      setLanguage(stored as Language);
    }
  }, []);

  function choose(next: Language) {
    setLanguage(next);
    try {
      localStorage.setItem(storageKey, next);
    } catch {}
  }

  return (
    <div>
      <fieldset className="m-0 mb-6 flex w-fit min-w-0 border-[1.5px] border-rule p-0">
        <legend className="sr-only">Language</legend>
        {languages.map((option) => (
          <label
            key={option}
            className={cn(
              "cursor-pointer border-r border-border px-3.5 py-2 font-mono text-[12.5px] whitespace-nowrap transition-colors last:border-r-0 has-focus-visible:outline-2 has-focus-visible:-outline-offset-2 has-focus-visible:outline-electric",
              option === language
                ? "bg-foreground text-background"
                : "bg-background text-muted-foreground hover:text-foreground",
            )}
          >
            <input
              type="radio"
              name="sdk-language"
              value={option}
              checked={option === language}
              onChange={() => choose(option)}
              className="sr-only"
            />
            {option}
          </label>
        ))}
      </fieldset>

      <div className="grid grid-cols-[minmax(0,1fr)] gap-4 lg:grid-cols-2">
        {primitives.map((primitive) => {
          const code = primitive.code[language];
          const Glyph = icons[primitive.name];
          return (
            <article
              key={primitive.name}
              className="flex min-w-0 flex-col border-[1.5px] border-rule bg-background"
            >
              <header className="flex items-center gap-4 border-b border-border px-5 py-4">
                <Glyph
                  aria-hidden
                  size={26}
                  weight="light"
                  className="shrink-0 text-muted-foreground"
                />
                <div className="min-w-0 flex-1">
                  <h3 className="m-0 font-mono text-[15px] font-medium text-foreground">
                    {primitive.name}
                  </h3>
                  <p className="m-0 mt-0.5 text-[14px] leading-snug text-muted-foreground">
                    {primitive.what}
                  </p>
                </div>
                <a
                  href={primitive.docs}
                  className="shrink-0 self-start text-[13px] text-muted-foreground transition-colors hover:text-foreground"
                >
                  Docs
                </a>
              </header>
              <div className="flex flex-1 flex-col bg-terminal">
                <CodePane pane={code.declare} />
                <div className="mt-auto border-t border-terminal-rule">
                  <CodePane pane={code.use} />
                </div>
              </div>
            </article>
          );
        })}
      </div>
    </div>
  );
}
