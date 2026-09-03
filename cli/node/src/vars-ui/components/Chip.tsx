import { cva, type VariantProps } from "class-variance-authority";
import type { ComponentProps } from "react";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

const chipVariants = cva(
  "gap-1 px-1.5 py-px font-mono text-[11px] font-normal tracking-normal normal-case [&>svg]:size-3!",
  {
    variants: {
      tone: {
        default: "bg-chip text-foreground",
        muted: "bg-chip text-muted-foreground",
        owed: "bg-destructive/10 text-destructive",
        ink: "bg-foreground text-background",
        accent: "bg-primary/10 text-primary",
      },
    },
    defaultVariants: { tone: "default" },
  },
);

export function Chip({
  className,
  tone,
  ...props
}: ComponentProps<typeof Badge> & VariantProps<typeof chipVariants>) {
  return <Badge className={cn(chipVariants({ tone }), className)} {...props} />;
}

export function ChipButton({
  className,
  tone,
  ...props
}: ComponentProps<"button"> & VariantProps<typeof chipVariants>) {
  return (
    <button
      type="button"
      className={cn(
        "inline-flex w-fit shrink-0 items-center justify-center whitespace-nowrap outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/30",
        chipVariants({ tone }),
        className,
      )}
      {...props}
    />
  );
}

export function SectionLabel({ className, ...props }: ComponentProps<"p">) {
  return (
    <p
      className={cn("mb-2 font-mono text-[11px] tracking-[0.1em] text-dim uppercase", className)}
      {...props}
    />
  );
}
