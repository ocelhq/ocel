import type { ReactNode } from "react";
import { role } from "../lib/type";
import { cn } from "../lib/utils";
import { plural } from "../model";
import { useValue } from "../signals";
import { dirty, discard, outcome, save, saving } from "../store";
import { Button } from "./ui/button";

export function SaveBar({ actions, note }: { actions?: ReactNode; note?: string | null }) {
  const pending = useValue(dirty).length;
  const busy = useValue(saving);
  const said = useValue(outcome);
  const text = [said?.text, note].filter(Boolean).join(" ");
  if (!actions && pending === 0 && text === "") return null;
  const tone = said?.tone ?? (note ? "owed" : undefined);
  return (
    <footer
      data-slot="bar"
      className="sticky bottom-0 z-20 flex flex-wrap items-center gap-x-6 gap-y-2 border-t border-border bg-background px-6 py-3"
    >
      <div className="flex flex-1 flex-wrap items-center gap-3">
        {pending > 0 && (
          <>
            <span className={cn(role.body, "tabular-nums")}>
              {plural(pending, "unsaved change")}
            </span>
            <Button
              variant={actions ? "outline" : "default"}
              size="sm"
              data-action="save"
              disabled={busy}
              onClick={() => void save()}
            >
              {busy ? "Saving…" : "Save"}
            </Button>
            <Button variant="ghost" size="sm" disabled={busy} onClick={discard}>
              Discard
            </Button>
          </>
        )}
        <p
          aria-live="polite"
          className={cn(role.body, tone === "owed" ? "text-destructive" : "text-body")}
        >
          {text}
        </p>
      </div>
      {actions}
    </footer>
  );
}
