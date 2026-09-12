import type { ReactNode } from "react";
import { Stamp } from "../../../stamp";

const termLabel =
  "font-sans text-[11px] font-medium tracking-[0.14em] text-muted-foreground uppercase";

function Fact({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5">
      <dt className={termLabel}>{term}</dt>
      <dd className="font-sans text-[13px] text-foreground">{children}</dd>
    </div>
  );
}

export function Provenance({
  deployedAt,
  now,
  promotion,
  provider,
  region,
  live,
}: {
  deployedAt: string;
  now: string;
  promotion: string | null;
  provider: string | null;
  region: string | null;
  live: boolean;
}) {
  return (
    <dl className="-mt-2 flex flex-wrap items-start gap-x-8 gap-y-3 border-b border-border pb-4">
      <Fact term="deployed">
        <Stamp at={deployedAt} now={now} />
      </Fact>
      {promotion && <Fact term="promotion">{promotion}</Fact>}
      {provider && (
        <Fact term="provider">
          {provider}
          {region && <span className="text-muted-foreground"> · {region}</span>}
        </Fact>
      )}
      <Fact term="values">
        <span className={live ? "text-go" : "text-muted-foreground"}>
          {live ? "live from your cloud" : "not readable"}
        </span>
      </Fact>
    </dl>
  );
}
