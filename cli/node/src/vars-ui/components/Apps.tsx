import { cn } from "@/lib/utils";

import { folderName, type State } from "../model";
import { hoveredApp } from "../store";
import { Glyph, SectionLabel } from "./Chip";

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
              className="flex cursor-default items-center gap-2.5 border border-border bg-card px-3 py-1.5 font-mono text-[12.5px] hover:border-foreground"
              onMouseEnter={() => (hoveredApp.value = app)}
              onMouseLeave={() => (hoveredApp.value = null)}
            >
              <span className="text-foreground">{app.name}</span>
              <span className="text-muted-foreground">reads {folderName(app.folder)}, then root</span>
              <span
                className={cn(
                  "inline-flex items-center gap-1.5",
                  missing.length === 0 ? "text-held" : "text-destructive",
                )}
              >
                <Glyph className="text-[9px]">●</Glyph>
                {missing.length === 0 ? "resolves" : `${missing.length} unresolved`}
              </span>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
