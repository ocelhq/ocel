import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

import { doneLabel, owedCount, tallyLine, type State } from "../model";
import { useValue } from "../signals";
import { dirty, finishing, leave, leaveDiscarding, saving } from "../store";
import { Glyph, SectionLabel } from "./Chip";

export function Masthead({ current }: { current: State }) {
  const owed = owedCount(current);
  const recovery = current.recovery !== undefined;
  const pending = useValue(dirty).length;
  const busy = useValue(saving) || useValue(finishing);
  return (
    <header className="mb-6 flex flex-wrap items-start justify-between gap-4 border-b-[1.5px] border-foreground pb-6">
      <div>
        <SectionLabel className="mb-1">Variables</SectionLabel>
        <h1 className="font-sans text-[34px] leading-[1.15] font-semibold tracking-[-0.02em]">
          {current.slug}{" "}
          <span className="font-mono text-lg font-normal tracking-normal text-muted-foreground">
            · {current.tier}
          </span>
        </h1>
        <p
          data-slot="tally"
          className={cn(
            "mt-2 inline-flex items-center gap-1.5 font-mono text-[13px]",
            owed === 0 ? "text-held" : "text-destructive",
          )}
        >
          {owed === 0 ? <Glyph>✓</Glyph> : <Glyph>●</Glyph>}
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
