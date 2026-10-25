"use client";

import { useTarget } from "@/components/landing/target";
import { targets } from "@/components/landing/transcripts";
import { cn } from "@/lib/utils";

function current(id: string) {
  return targets.find((t) => t.id === id) ?? targets[0];
}

export function Needs() {
  const { target } = useTarget();
  const t = current(target);
  return (
    <p className="m-0 border-t border-terminal-rule px-4 py-3 font-mono text-[12px] leading-relaxed text-terminal-foreground sm:px-5">
      <span className="tracking-[0.14em] text-[#7d8590] uppercase">Needs </span>
      <span className="text-terminal-ink">{t.needs}</span>, because {t.why}.
      {t.id === "cloud" ? (
        <>
          {" "}
          <button
            type="button"
            onClick={() => document.getElementById("early-access")?.focus()}
            className="text-terminal-ink underline decoration-[oklch(0.587_0.225_270.16)] decoration-[1.5px] underline-offset-4"
          >
            Request one
          </button>
        </>
      ) : null}
    </p>
  );
}

export function TargetName({ className }: { className?: string }) {
  const { target } = useTarget();
  return <span className={className}>{current(target).name}</span>;
}

export function ConfigDiff() {
  const { target, setTarget } = useTarget();
  const t = current(target);
  const lines: { mark: " " | "+"; text: string }[] = [
    { mark: " ", text: "{" },
    { mark: " ", text: `  "slug": "notes",` },
    ...(t.config ? [{ mark: "+" as const, text: `  ${t.config}` }] : []),
    { mark: " ", text: `  "apps": [{ "name": "web", "path": "." }]` },
    { mark: " ", text: "}" },
  ];

  return (
    <div className="flex min-w-0 flex-col">
      <fieldset className="m-0 grid min-w-0 grid-cols-2 border-[1.5px] border-b-0 border-foreground p-0 sm:flex">
        <legend className="sr-only">Target</legend>
        {targets.map((option) => (
          <label
            key={option.id}
            className={cn(
              "flex-1 cursor-pointer border-r border-b border-border px-3 py-2.5 text-center font-mono text-[12px] whitespace-nowrap transition-colors even:border-r-0 has-focus-visible:outline-2 has-focus-visible:-outline-offset-2 has-focus-visible:outline-electric sm:border-b-0 sm:even:border-r sm:last:border-r-0",
              option.id === target
                ? "bg-foreground text-background"
                : "bg-background text-muted-foreground hover:text-foreground",
            )}
          >
            <input
              type="radio"
              name="config-target"
              value={option.id}
              checked={option.id === target}
              onChange={() => setTarget(option.id)}
              className="sr-only"
            />
            {option.name}
          </label>
        ))}
      </fieldset>
      <figure className="m-0 border-[1.5px] border-foreground bg-terminal shadow-float">
        <figcaption className="flex items-center justify-between border-b border-terminal-rule px-4 py-2 font-mono text-[11px] text-terminal-foreground">
          <span>ocel.json</span>
          <span className="tracking-[0.14em] uppercase">
            {t.config ? "1 line changed" : "no provider: Ocel Cloud"}
          </span>
        </figcaption>
        <pre className="m-0 overflow-x-auto py-3 font-mono text-[13px] leading-[1.85]">
          {lines.map((line) => (
            <span
              key={`${line.mark}${line.text}`}
              className={cn(
                "block px-4",
                line.mark === "+"
                  ? "bg-[#3ecf7a]/10 text-terminal-ink"
                  : "text-terminal-foreground",
              )}
            >
              <span
                className={cn(
                  "mr-3 inline-block w-2 select-none",
                  line.mark === "+" ? "text-[#3ecf7a]" : "text-[#7d8590]",
                )}
              >
                {line.mark}
              </span>
              {line.text}
            </span>
          ))}
        </pre>
      </figure>
    </div>
  );
}
