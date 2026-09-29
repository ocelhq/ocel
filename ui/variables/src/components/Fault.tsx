import { WarningIcon } from "@phosphor-icons/react";
import type { ReactNode } from "react";
import { glyph, role } from "../lib/type";
import { cn } from "../lib/utils";

export function Fault({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <span className={cn(role.meta, "inline-flex items-start gap-1.5 text-destructive", className)}>
      <WarningIcon className={cn(glyph.inline, "mt-px shrink-0")} />
      <span>{children}</span>
    </span>
  );
}
