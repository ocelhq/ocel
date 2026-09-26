import { CheckCircleIcon, WarningCircleIcon } from "@phosphor-icons/react";
import {
  Button,
  doneLabel,
  glyph,
  owedCount,
  role,
  SectionLabel,
  type State,
  store,
  tallyLine,
  useValue,
} from "@ui/vars";

import { cn } from "../lib/utils";

export function Masthead({ current }: { current: State }) {
  const unfilled = owedCount(current);
  const recovery = current.recovery !== undefined;
  const pending = useValue(store.dirty).length;
  const isSaving = useValue(store.saving);
  const isFinishing = useValue(store.finishing);
  const busy = isSaving || isFinishing;
  return (
    <header className="mb-6 flex flex-wrap items-start justify-between gap-4 border-b border-border pb-6">
      <div>
        <SectionLabel className="mb-1">Variables</SectionLabel>
        <h1 className="font-heading text-[34px] leading-[1.15] font-semibold tracking-[-0.02em]">
          {current.slug}{" "}
          <span className="font-sans text-lg font-normal tracking-normal text-muted-foreground">
            · {current.tier}
          </span>
        </h1>
        <p
          data-slot="tally"
          className={cn(
            role.body,
            "mt-2 inline-flex items-center gap-1.5",
            unfilled === 0 ? "text-go" : "text-warn",
          )}
        >
          {unfilled === 0 ? (
            <CheckCircleIcon weight="fill" className={glyph.control} />
          ) : (
            <WarningCircleIcon weight="fill" className={glyph.control} />
          )}
          {tallyLine(unfilled)}
        </p>
      </div>
      {!recovery &&
        (pending > 0 ? (
          <Button variant="outline" size="sm" disabled={busy} onClick={store.leaveDiscarding}>
            Return without saving
          </Button>
        ) : (
          <Button variant="outline" size="sm" disabled={busy} onClick={store.leave}>
            {doneLabel(unfilled)}
          </Button>
        ))}
    </header>
  );
}
