import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

const filters = ["All", "Live", "Torn down", "Archived"];

export function ProjectsShell({ children }: { children: ReactNode }) {
  return (
    <div className="flex flex-1 flex-col gap-6 px-5 pt-8 pb-12 md:px-8">
      <div className="flex flex-col gap-4">
        <h1 className="text-2xl font-semibold tracking-tight text-balance">Projects</h1>
        <fieldset className="flex flex-wrap gap-2">
          <legend className="sr-only">Filter projects</legend>
          {filters.map((filter, index) => (
            <button
              key={filter}
              type="button"
              aria-pressed={index === 0}
              className={cn(
                "h-7 border border-dashed px-2.5 font-mono text-[11px] font-medium tracking-[0.14em] uppercase transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring/30",
                index === 0
                  ? "border-foreground text-foreground"
                  : "border-dim text-muted-foreground hover:border-foreground hover:text-foreground",
              )}
            >
              {filter}
            </button>
          ))}
        </fieldset>
      </div>

      {children}
    </div>
  );
}

export function ProjectGrid({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <ul className={cn("grid grid-cols-1 pt-px pl-px md:grid-cols-2 xl:grid-cols-3", className)}>
      {children}
    </ul>
  );
}

export function ProjectGridCell({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <li className={cn("-mt-px -ml-px min-w-0 border border-border", className)}>{children}</li>
  );
}
