import type { ReactNode } from "react";
import { cn } from "@/lib/utils";
import { PageShell } from "../page-shell";

export function ProjectsShell({ children }: { children: ReactNode }) {
  return <PageShell title="Projects">{children}</PageShell>;
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
