import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

export function Cursor() {
  return (
    <span
      aria-hidden
      className="ml-1 inline-block h-4 w-[9px] -translate-y-[2px] animate-blink bg-primary align-middle"
    />
  );
}

type TerminalProps = {
  title: string;
  children: ReactNode;
  className?: string;
  bodyClassName?: string;
  onClick?: () => void;
};

export function Terminal({
  title,
  children,
  className,
  bodyClassName,
  onClick,
}: TerminalProps) {
  const interactive = onClick
    ? {
        role: "button" as const,
        tabIndex: 0,
        onClick,
        onKeyDown: (e: React.KeyboardEvent) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            onClick();
          }
        },
      }
    : {};

  return (
    <div
      {...interactive}
      className={cn(
        "min-w-0 max-w-full border-[1.5px] border-foreground bg-card shadow-[6px_6px_0_0_var(--hard-shadow)]",
        onClick && "cursor-pointer",
        className,
      )}
    >
      <div className="flex items-center gap-1.5 border-b-[1.5px] border-foreground px-3.5 py-2.5">
        <span className="size-[9px] shrink-0 rounded-full border-[1.5px] border-foreground" />
        <span className="size-[9px] shrink-0 rounded-full border-[1.5px] border-foreground" />
        <span className="size-[9px] shrink-0 rounded-full bg-primary" />
        <span className="ml-2 truncate font-mono text-[11px] text-dim">
          {title}
        </span>
      </div>
      <div
        className={cn("overflow-x-auto px-5.5 py-4.5 font-mono", bodyClassName)}
      >
        {children}
      </div>
    </div>
  );
}
