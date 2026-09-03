import type { ComponentProps } from "react";

import { Badge, badgeVariants } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

const tones = {
  default: "default",
  muted: "secondary",
  owed: "destructive",
  accent: "outline",
  soon: "ghost",
  ink: "default",
} as const;

export type Tone = keyof typeof tones;

const inked = "border-foreground bg-foreground text-background";

export function Chip({
  className,
  tone = "default",
  ...props
}: ComponentProps<typeof Badge> & { tone?: Tone }) {
  return (
    <Badge
      variant={tones[tone]}
      className={cn(tone === "ink" && inked, className)}
      {...props}
    />
  );
}

export function ChipButton({
  className,
  tone = "default",
  ...props
}: ComponentProps<"button"> & { tone?: Tone }) {
  return (
    <button
      type="button"
      className={cn(
        badgeVariants({ variant: tones[tone] }),
        "cursor-pointer outline-none hover:border-primary hover:text-primary focus-visible:ring-2 focus-visible:ring-ring",
        tone === "ink" && inked,
        className,
      )}
      {...props}
    />
  );
}

export function SectionLabel({ className, ...props }: ComponentProps<"p">) {
  return (
    <p
      className={cn(
        "mb-2 font-mono text-xs tracking-[0.14em] text-muted-foreground uppercase",
        className,
      )}
      {...props}
    />
  );
}

export function Glyph({ className, ...props }: ComponentProps<"span">) {
  return <span aria-hidden="true" className={cn("font-mono", className)} {...props} />;
}

export function Note({
  label,
  className,
  children,
  ...props
}: ComponentProps<"div"> & { label: string }) {
  return (
    <div
      className={cn(
        "border border-border border-t-2 border-t-primary bg-card px-4.5 py-3.5 font-sans text-[13.5px] leading-relaxed text-body",
        className,
      )}
      {...props}
    >
      <p className="mb-1 font-mono text-[11px] tracking-[0.1em] text-primary uppercase">{label}</p>
      {children}
    </div>
  );
}
