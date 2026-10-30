"use client";

import { CheckIcon, ClipboardDocumentIcon } from "@heroicons/react/24/outline";
import { type KeyboardEvent, type ReactNode, useEffect, useId, useRef, useState } from "react";
import { capture } from "@/lib/analytics";

export type InstallMethod = { name: string; command: string; highlighted: ReactNode };

export function stepTab(index: number, key: string, count: number): number | undefined {
  if (key === "ArrowRight") return (index + 1) % count;
  if (key === "ArrowLeft") return (index - 1 + count) % count;
  if (key === "Home") return 0;
  if (key === "End") return count - 1;
  return undefined;
}

export function InstallCommand({ methods }: { methods: InstallMethod[] }) {
  const [selected, setSelected] = useState(0);
  const [copied, setCopied] = useState(false);
  const tabs = useRef<(HTMLButtonElement | null)[]>([]);
  const id = useId();
  const { name, command, highlighted } = methods[selected];

  function choose(index: number) {
    setSelected(index);
    setCopied(false);
  }

  function step(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    const next = stepTab(index, event.key, methods.length);
    if (next === undefined) return;
    event.preventDefault();
    choose(next);
    tabs.current[next]?.focus();
  }

  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(timer);
  }, [copied]);

  async function copy() {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      capture("cli_install_command_copied", { command, method: name });
    } catch {
      setCopied(false);
    }
  }

  return (
    <div className="mt-8 w-full max-w-md border border-(--hairline) bg-(--tile)">
      <div
        role="tablist"
        aria-label="Install with"
        className="flex gap-5 border-b border-(--hairline) px-4"
      >
        {methods.map((method, index) => (
          <button
            key={method.name}
            ref={(element) => {
              tabs.current[index] = element;
            }}
            type="button"
            role="tab"
            id={`${id}-tab-${index}`}
            aria-controls={`${id}-panel`}
            aria-selected={index === selected}
            tabIndex={index === selected ? 0 : -1}
            onClick={() => choose(index)}
            onKeyDown={(event) => step(event, index)}
            className="-mb-px border-b border-transparent py-2.5 font-mono text-[0.6875rem] font-medium uppercase leading-none tracking-[0.14em] text-(--body) transition-colors hover:text-(--ink) aria-selected:border-(--electric) aria-selected:text-(--ink) focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-(--electric)/30"
          >
            {method.name}
          </button>
        ))}
      </div>
      <div
        role="tabpanel"
        id={`${id}-panel`}
        aria-labelledby={`${id}-tab-${selected}`}
        className="flex items-center gap-3 py-2 ps-4 pe-2"
      >
        <code className="shiki min-w-0 [&_.line]:p-0! flex-1 overflow-x-auto whitespace-nowrap font-mono text-[0.8125rem] leading-[1.9] text-(--ink) [scrollbar-width:none]">
          <span className="select-none text-(--steel)!">$ </span>
          {highlighted}
        </code>
        <button
          type="button"
          onClick={copy}
          aria-label={copied ? "Copied" : "Copy install command"}
          className="shrink-0 p-1.5 text-(--steel) transition-colors hover:text-(--ink) focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-(--electric)/30"
        >
          {copied ? (
            <CheckIcon className="size-4 text-(--go)" aria-hidden="true" />
          ) : (
            <ClipboardDocumentIcon className="size-4" aria-hidden="true" />
          )}
        </button>
        <span className="sr-only" aria-live="polite">
          {copied ? "Copied to clipboard" : ""}
        </span>
      </div>
    </div>
  );
}
