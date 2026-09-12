import type { ComponentProps } from "react";
import { role } from "../lib/type";
import { cn } from "../lib/utils";
import { Badge, badgeVariants } from "./ui/badge";

const tones = {
  default: "outline",
  muted: "secondary",
  owed: "warn",
  bad: "destructive",
  accent: "outline",
  soon: "ghost",
  ink: "default",
} as const;

export type Tone = keyof typeof tones;

export function Chip({
  className,
  tone = "default",
  ...props
}: ComponentProps<typeof Badge> & { tone?: Tone }) {
  return (
    <Badge variant={tones[tone]} className={cn("font-sans font-normal", className)} {...props} />
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
        "cursor-pointer font-sans font-normal outline-none hover:bg-muted focus-visible:border-ring focus-visible:ring-1 focus-visible:ring-ring/50",
        className,
      )}
      {...props}
    />
  );
}

export function SectionLabel({ className, ...props }: ComponentProps<"p">) {
  return <p className={cn(role.label, "mb-2 text-muted-foreground", className)} {...props} />;
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
        role.body,
        "border border-border bg-card px-4 py-3 leading-relaxed text-muted-foreground",
        className,
      )}
      {...props}
    >
      <p className={cn(role.label, "mb-1 text-foreground")}>{label}</p>
      {children}
    </div>
  );
}
