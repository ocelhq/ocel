import { CheckIcon } from "@phosphor-icons/react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

import { doneLabel, owedCount, tallyLine, type State } from "../model";
import { useValue } from "../signals";
import { dirty, finishing, leave, leaveDiscarding, saving } from "../store";

export function Masthead({ current }: { current: State }) {
  const owed = owedCount(current);
  const recovery = current.recovery !== undefined;
  const pending = useValue(dirty).length;
  const busy = useValue(saving) || useValue(finishing);
  return (
    <header className="mb-6 flex flex-wrap items-start justify-between gap-4">
      <div>
        <p className="mb-1 font-mono text-[11px] tracking-[0.1em] text-dim uppercase">Variables</p>
        <h1 className="font-mono text-2xl font-semibold tracking-tight">
          {current.slug} <span className="font-normal text-muted-foreground">· {current.tier}</span>
        </h1>
        <p
          data-slot="tally"
          className={cn(
            "mt-1 inline-flex items-center gap-1.5 font-mono text-xs",
            owed === 0 ? "text-held" : "text-destructive",
          )}
        >
          {owed === 0 && <CheckIcon className="size-3.5" weight="bold" />}
          {tallyLine(owed)}
        </p>
      </div>
      {!recovery &&
        (pending > 0 ? (
          <Button variant="outline" size="sm" disabled={busy} onClick={leaveDiscarding}>
            Return without saving
          </Button>
        ) : (
          <Button variant="outline" size="sm" disabled={busy} onClick={leave}>
            {doneLabel(owed)}
          </Button>
        ))}
    </header>
  );
}
