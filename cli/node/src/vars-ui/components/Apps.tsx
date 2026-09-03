import { AppWindowIcon } from "@phosphor-icons/react";

import { cn } from "@/lib/utils";

import { folderName, type State } from "../model";
import { hoveredApp } from "../store";
import { SectionLabel } from "./Chip";

export function Apps({ current }: { current: State }) {
  if (current.matrix.apps.length === 0) return null;
  return (
    <section className="mb-6">
      <SectionLabel>Apps</SectionLabel>
      <ul className="flex flex-wrap gap-2">
        {current.matrix.apps.map((app) => {
          const missing = app.missing ?? [];
          return (
            <li
              key={app.name}
              className="flex cursor-default items-center gap-2 border border-border bg-card px-3 py-1.5 text-xs hover:border-foreground"
              onMouseEnter={() => (hoveredApp.value = app)}
              onMouseLeave={() => (hoveredApp.value = null)}
            >
              <AppWindowIcon className="size-4 text-muted-foreground" />
              <span className="font-mono font-medium">{app.name}</span>
              <span className="text-muted-foreground">reads {folderName(app.folder)}, then root</span>
              <span
                className={cn(
                  "font-mono text-[11px]",
                  missing.length === 0 ? "text-held" : "text-destructive",
                )}
              >
                {missing.length === 0 ? "resolves" : `${missing.length} unresolved`}
              </span>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
