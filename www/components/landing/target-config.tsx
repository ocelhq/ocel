"use client";

import { useState } from "react";
import { Frame } from "@/components/landing/frame";
import type { TargetId } from "@/components/landing/targets";
import { type Line, Transcript } from "@/components/landing/transcript";
import { cn } from "@/lib/utils";

export type RenderedTarget = { id: TargetId; label: string; html: string; output: Line[] };

export function TargetConfig({ targets }: { targets: RenderedTarget[] }) {
  const [id, setId] = useState<TargetId>(targets[0]?.id ?? "cloud");
  const target = targets.find((t) => t.id === id) ?? targets[0];
  if (!target) return null;

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <div>
        <fieldset className="m-0 grid min-w-0 grid-cols-2 border-[1.5px] border-b-0 border-foreground p-0 sm:flex">
          <legend className="sr-only">Target</legend>
          {targets.map((option) => (
            <label
              key={option.id}
              className={cn(
                "flex-1 cursor-pointer border-r border-b border-border px-3 py-2.5 text-center font-mono text-[12.5px] whitespace-nowrap transition-colors even:border-r-0 has-focus-visible:outline-2 has-focus-visible:-outline-offset-2 has-focus-visible:outline-electric sm:border-b-0 sm:even:border-r sm:last:border-r-0",
                option.id === id
                  ? "bg-foreground text-background"
                  : "bg-background text-muted-foreground hover:text-foreground",
              )}
            >
              <input
                type="radio"
                name="deploy-target"
                value={option.id}
                checked={option.id === id}
                onChange={() => setId(option.id)}
                className="sr-only"
              />
              {option.label}
            </label>
          ))}
        </fieldset>
        <Frame kind="terminal" title="ocel.config.ts">
          <div
            className="shiki-pane min-h-[15.5rem] px-4 py-3.5"
            dangerouslySetInnerHTML={{ __html: target.html }}
          />
        </Frame>
      </div>
      <Frame kind="terminal" title="~/notes">
        <Transcript lines={target.output} />
      </Frame>
    </div>
  );
}
