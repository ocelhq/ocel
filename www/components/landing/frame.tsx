import type { ReactNode } from "react";
import type { ClipKind } from "@/components/landing/clips";
import { cn } from "@/lib/utils";

function Dots() {
  return (
    <>
      <span className="size-2.5 shrink-0 rounded-full bg-[#ff5f57]" />
      <span className="size-2.5 shrink-0 rounded-full bg-[#febc2e]" />
      <span className="size-2.5 shrink-0 rounded-full bg-[#28c840]" />
    </>
  );
}

// A window set into the sheet: a terminal (always dark) or a browser (paper), framed in
// 1.5px Ink with the print offset. The title bar names what is in it.
export function Frame({
  kind,
  title,
  aside,
  children,
  className,
  flat,
}: {
  kind: ClipKind;
  title: ReactNode;
  aside?: ReactNode;
  children: ReactNode;
  className?: string;
  /** Drop the print offset, for a frame stacked on another. */
  flat?: boolean;
}) {
  const dark = kind === "terminal";
  return (
    <figure
      className={cn(
        "m-0 min-w-0 border-[1.5px] border-foreground",
        dark ? "bg-terminal" : "bg-background",
        !flat && "shadow-float",
        className,
      )}
    >
      <div
        className={cn(
          "flex min-h-10 items-center gap-1.5 border-b px-3.5 py-2",
          dark ? "border-terminal-rule" : "border-border bg-muted",
        )}
      >
        <Dots />
        <figcaption
          className={cn(
            "ml-2 min-w-0 truncate font-mono text-[11px]",
            dark ? "text-terminal-foreground" : "text-muted-foreground",
          )}
        >
          {title}
        </figcaption>
        {aside ? <div className="ml-auto flex shrink-0 items-center pl-3">{aside}</div> : null}
      </div>
      {children}
    </figure>
  );
}
