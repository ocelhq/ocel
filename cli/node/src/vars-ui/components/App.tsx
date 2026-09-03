import {
  EyeIcon,
  EyeSlashIcon,
  FileTextIcon,
  TrashIcon,
  WarningIcon,
  XIcon,
} from "@phosphor-icons/react";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { cn } from "@/lib/utils";

import { folderName, names, plural } from "../model";
import { useValue } from "../signals";
import {
  abandon,
  askRemoval,
  cancelRemoval,
  clearSelection,
  confirmRemoval,
  dirty,
  discard,
  dismissDrop,
  dropped,
  farewell,
  finishError,
  finishing,
  hideSelected,
  outcome,
  owedOnly,
  removing,
  resume,
  revealSelected,
  save,
  saving,
  selected,
  showOwed,
  state,
  unfilled,
  variants,
} from "../store";
import { Apps } from "./Apps";
import { CopyPanel } from "./CopyPanel";
import { Drawer } from "./Drawer";
import { Masthead } from "./Masthead";
import { Table } from "./Table";

function DropNotice() {
  const drop = useValue(dropped);
  if (!drop) return null;
  const where = folderName(drop.folder);
  return (
    <div
      role="status"
      data-slot="notice"
      className="mb-4 flex items-start gap-3 border border-border bg-card px-4 py-3 text-sm"
    >
      <FileTextIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
      <div className="flex-1 space-y-1">
        <p>
          <strong className="font-mono font-medium">{drop.name}</strong>:{" "}
          {drop.fills.length === 0
            ? `nothing to fill in ${where}.`
            : `${plural(drop.fills.length, "row")} filled in ${where}, unsaved until you save.`}
        </p>
        {drop.undeclared.length > 0 && (
          <p className="text-muted-foreground">
            Ignored {plural(drop.undeclared.length, "key")} this project does not declare:{" "}
            <code className="bg-chip px-1 text-xs">{names(drop.undeclared)}</code>. Keys come from{" "}
            <code className="bg-chip px-1 text-xs">defineEnv</code> in app code; this page cannot
            create one.
          </p>
        )}
        {drop.skipped.map((skip) => (
          <p className="text-muted-foreground" key={skip.key}>
            Skipped {skip.key}: {skip.reason}.
          </p>
        ))}
      </div>
      <Button
        variant="ghost"
        size="icon-xs"
        title="Dismiss"
        aria-label="dismiss this notice"
        onClick={dismissDrop}
      >
        <XIcon />
      </Button>
    </div>
  );
}

function Banner() {
  const current = useValue(state);
  const left = useValue(unfilled).length;
  const only = useValue(owedOnly);
  const recovery = current?.recovery;
  if (!current || !recovery) return null;
  return (
    <div
      role="status"
      data-slot="banner"
      className="mb-4 flex flex-wrap items-center gap-x-6 gap-y-2 border border-destructive/40 bg-destructive/5 px-4 py-3 text-sm"
    >
      <WarningIcon className="size-4 shrink-0 text-destructive" />
      <p className="flex-1 basis-96">
        Deploy <code className="bg-chip px-1 text-xs">{recovery.deploy}</code> is waiting on{" "}
        {plural(recovery.owed.length, "cell")} it was refused.{" "}
        {left === 0
          ? "Every one now holds a value; save and resume below."
          : `${plural(left, "cell")} still ${left === 1 ? "needs" : "need"} a value.`}
      </p>
      <label className="inline-flex cursor-pointer items-center gap-2 text-xs">
        <Checkbox checked={only} onCheckedChange={(on) => showOwed(on)} />
        Show only what the deploy needs
      </label>
    </div>
  );
}

function BulkBar() {
  const picked = useValue(selected);
  const known = useValue(variants);
  const busy = useValue(saving);
  if (picked.size === 0) return null;
  const cells = [...picked]
    .map((key) => known.get(key))
    .filter((v) => v !== undefined)
    .map((v) => v!.at);
  const removable = cells.filter((at) => {
    const v = known.get(`${at.key} ${at.folder} ${at.environment}`);
    return v !== undefined && v.set && !v.reference;
  });
  return (
    <div
      role="toolbar"
      aria-label="selected rows"
      data-slot="bulk"
      className="sticky bottom-0 z-20 flex flex-wrap items-center gap-2 border-t border-border bg-background px-6 py-2.5"
    >
      <span className="font-mono text-xs">{picked.size} selected</span>
      <Button variant="ghost" size="xs" data-action="unselect" onClick={clearSelection}>
        Unselect all
      </Button>
      <span className="flex-1" />
      <Button variant="outline" size="xs" onClick={() => void revealSelected()}>
        <EyeIcon />
        Reveal
      </Button>
      <Button variant="outline" size="xs" onClick={hideSelected}>
        <EyeSlashIcon />
        Hide
      </Button>
      <Button
        variant="destructive"
        size="xs"
        data-action="remove"
        disabled={removable.length === 0 || busy}
        onClick={() => askRemoval(removable)}
      >
        <TrashIcon />
        Remove {removable.length > 0 ? plural(removable.length, "value") : "values"}
      </Button>
    </div>
  );
}

function Confirm() {
  const asked = useValue(removing);
  const busy = useValue(saving);
  return (
    <AlertDialog open={asked !== null} onOpenChange={(open) => !open && cancelRemoval()}>
      {asked && (
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove {plural(asked.cells.length, "value")}?</AlertDialogTitle>
            <AlertDialogDescription>
              The stored value goes away for {names(asked.cells.map((cell) => cell.at.key))}.
              History keeps the versions; nothing else on this page is touched.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy} data-action="cancel-remove">
              Cancel
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={busy}
              data-action="confirm-remove"
              onClick={() => void confirmRemoval()}
            >
              <TrashIcon />
              {busy ? "Removing…" : "Remove"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      )}
    </AlertDialog>
  );
}

function Bar({ recovery }: { recovery: boolean }) {
  const pending = useValue(dirty).length;
  const busySaving = useValue(saving);
  const busyFinishing = useValue(finishing);
  const said = useValue(outcome);
  const failed = useValue(finishError);
  const left = useValue(unfilled).length;
  const busy = busySaving || busyFinishing;
  const text = [said?.text, failed].filter(Boolean).join(" ");
  if (!recovery && pending === 0 && text === "") return null;
  const tone = said?.tone ?? (failed ? "owed" : undefined);
  return (
    <footer
      data-slot="bar"
      className="sticky bottom-0 z-20 flex flex-wrap items-center gap-x-6 gap-y-2 border-t border-border bg-background px-6 py-3"
    >
      <div className="flex flex-1 flex-wrap items-center gap-3">
        {pending > 0 && (
          <>
            <span className="font-mono text-xs">{plural(pending, "unsaved change")}</span>
            <Button
              variant={recovery ? "outline" : "default"}
              size="sm"
              data-action="save"
              disabled={busy}
              onClick={() => void save()}
            >
              {busySaving ? "Saving…" : "Save"}
            </Button>
            <Button variant="ghost" size="sm" disabled={busy} onClick={discard}>
              Discard
            </Button>
          </>
        )}
        <p
          aria-live="polite"
          className={cn("text-sm", tone === "owed" ? "text-destructive" : "text-muted-foreground")}
        >
          {text}
        </p>
      </div>
      {recovery && (
        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            data-action="resume"
            disabled={busy || left > 0}
            title={
              left > 0
                ? `${plural(left, "cell")} the deploy needs ${left === 1 ? "is" : "are"} still empty or invalid`
                : undefined
            }
            onClick={() => void resume()}
          >
            {busyFinishing ? "Resuming…" : "Save and resume the deploy"}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="text-destructive hover:text-destructive"
            disabled={busy}
            onClick={() => void abandon()}
          >
            Abandon the deploy
          </Button>
        </div>
      )}
    </footer>
  );
}

export function App() {
  const goodbye = useValue(farewell);
  const current = useValue(state);
  if (goodbye !== null) {
    return <p className="p-12 font-mono text-sm text-muted-foreground">{goodbye}</p>;
  }
  if (!current) {
    return (
      <p className="p-12 font-mono text-sm text-muted-foreground">
        Reading this project’s variables…
      </p>
    );
  }
  const recovery = current.recovery !== undefined;
  return (
    <div className="flex min-h-screen flex-col">
      <div className="mx-auto w-full max-w-[92rem] flex-1 px-6 pt-6 pb-24">
        <Masthead current={current} />
        <Banner />
        <Apps current={current} />
        <DropNotice />
        <Table />
      </div>
      <BulkBar />
      <Bar recovery={recovery} />
      <Drawer />
      <CopyPanel />
      <Confirm />
    </div>
  );
}
