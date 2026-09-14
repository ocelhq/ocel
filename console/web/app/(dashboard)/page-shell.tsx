import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export function PageShell({
  title,
  description,
  aside,
  children,
}: {
  title: ReactNode;
  description?: string;
  aside?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="flex flex-1 flex-col gap-6 px-5 pt-8 pb-12 md:px-8">
      <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-4">
        <div className="flex max-w-prose flex-col gap-1">
          <h1 className="text-2xl font-semibold tracking-tight text-balance">{title}</h1>
          {description && <p className="text-muted-foreground">{description}</p>}
        </div>
        {aside}
      </div>
      {children}
    </div>
  );
}

export function PageNotice({
  heading,
  role,
  className,
  children,
}: {
  heading?: string;
  role?: "alert";
  className?: string;
  children: ReactNode;
}) {
  return (
    <div
      role={role}
      className={cn(
        "flex max-w-2xl flex-col items-start gap-5 border border-border px-5 py-8 md:px-8",
        className,
      )}
    >
      {heading && <h2 className="text-lg font-semibold tracking-tight text-balance">{heading}</h2>}
      {children}
    </div>
  );
}

export const noticeBody = "max-w-prose text-muted-foreground";
