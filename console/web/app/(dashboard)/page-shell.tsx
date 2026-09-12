import type { ReactNode } from "react";

export function PageShell({
  title,
  description,
  children,
}: {
  title: string;
  description?: string;
  children?: ReactNode;
}) {
  return (
    <div className="flex flex-1 flex-col gap-6 px-5 pt-8 pb-12 md:px-8">
      <div className="flex max-w-prose flex-col gap-1">
        <h1 className="text-2xl font-semibold tracking-tight text-balance">{title}</h1>
        {description && <p className="text-muted-foreground">{description}</p>}
      </div>
      {children}
    </div>
  );
}

export function PageNotice({ children }: { children: ReactNode }) {
  return (
    <div className="flex max-w-2xl flex-col items-start gap-5 border border-border px-5 py-10 md:px-10">
      {children}
    </div>
  );
}
