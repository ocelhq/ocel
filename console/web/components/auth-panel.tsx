import type { ReactNode } from "react";

export function AuthPanel({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children?: ReactNode;
}) {
  return (
    <div className="flex flex-1 items-center justify-center bg-background px-4 py-12">
      <div className="flex w-full max-w-sm flex-col gap-6 border border-border bg-card p-8">
        <div className="flex flex-col gap-3">
          <span className="font-display text-xl leading-none font-extrabold tracking-[-0.04em] lowercase text-foreground">
            ocel
          </span>
          <h1 className="text-2xl leading-tight font-semibold tracking-[-0.025em] text-balance text-foreground">
            {title}
          </h1>
          <p className="text-sm text-muted-foreground">{description}</p>
        </div>
        {children}
      </div>
    </div>
  );
}

export function AuthError({ children }: { children: ReactNode }) {
  return (
    <p role="alert" className="text-sm text-destructive">
      {children}
    </p>
  );
}
