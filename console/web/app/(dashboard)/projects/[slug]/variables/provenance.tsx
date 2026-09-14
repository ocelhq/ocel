import type { ReactNode } from "react";
import { shortId } from "@/lib/runs";
import { labelType } from "@/lib/type";
import { Stamp } from "../../../stamp";

function Fact({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5">
      <dt className={labelType}>{term}</dt>
      <dd className="text-[13px] text-foreground">{children}</dd>
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
      {promotion && (
        <Fact term="promotion">
          <span className="font-mono" title={promotion}>
            {shortId(promotion)}
          </span>
        </Fact>
      )}
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
