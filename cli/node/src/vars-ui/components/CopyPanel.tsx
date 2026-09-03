import {
  CaretRightIcon,
  CopyIcon,
  FolderIcon,
  HouseIcon,
  KeyIcon,
  LockIcon,
  StackIcon,
  WarningIcon,
} from "@phosphor-icons/react";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { cn } from "@/lib/utils";

import {
  addressKey,
  copyTree,
  folderName,
  plural,
  type Address,
  type CopyBranch,
  type CopyCell,
} from "../model";
import { useValue } from "../signals";
import {
  baselines,
  closeCopy,
  confirmCopy,
  copying,
  state,
  toggleBranch,
  toggleCopy,
  toggleOverwriting,
} from "../store";
import { Chip, SectionLabel } from "./Chip";

export function CopyPanel() {
  const dialog = useValue(copying);
  const here = useValue(state)?.tier ?? "";
  return (
    <Sheet open={dialog !== null} onOpenChange={(open) => !open && closeCopy()}>
      {dialog && <Panel here={here} />}
    </Sheet>
  );
}

function Panel({ here }: { here: string }) {
  const dialog = useValue(copying)!;
  const { plan, chosen, busy } = dialog;
  const count = [...plan.fills, ...plan.overwrites].filter((cell) =>
    chosen.has(addressKey(cell.at)),
  ).length;
  const branches = copyTree(plan);
  const nothing = branches.length + plan.unreadable.length + plan.skipped.length === 0;
  return (
    <SheetContent data-slot="copy-panel" className="gap-0 sm:max-w-2xl">
      <SheetHeader className="border-b border-border p-6">
        <SheetTitle className="font-mono text-base normal-case tracking-tight">
          Copy from {dialog.tier}
        </SheetTitle>
        <SheetDescription>A one-time copy into {here}. Nothing stays linked.</SheetDescription>
      </SheetHeader>
      <div className="flex-1 overflow-y-auto p-6 text-sm">
        <div className="mb-6 flex items-center gap-4 border border-border p-3">
          <div className="flex flex-col">
            <span className="font-mono text-[11px] tracking-[0.1em] text-dim uppercase">Source</span>
            <span className="font-mono">{dialog.tier}</span>
          </div>
          <span className="font-mono text-dim" aria-hidden="true">
            →
          </span>
          <div className="flex flex-col">
            <span className="font-mono text-[11px] tracking-[0.1em] text-dim uppercase">
              Destination
            </span>
            <span className="font-mono text-primary">{here}</span>
          </div>
        </div>

        {nothing ? (
          <p className="text-muted-foreground">
            {dialog.tier} holds no value for a key this project declares.
          </p>
        ) : (
          <>
            <SectionLabel>Values to copy</SectionLabel>
            <label className="mb-3 inline-flex cursor-pointer items-center gap-2 text-xs">
              <Checkbox
                checked={dialog.overwriting}
                disabled={busy || plan.overwrites.length === 0}
                onCheckedChange={toggleOverwriting}
              />
              Overwrite values already set here
              {plan.overwrites.length > 0 && (
                <span className="text-muted-foreground">({plan.overwrites.length})</span>
              )}
            </label>
            <ul className="border-y border-border">
              {branches.map((branch) => (
                <Branch key={branch.folder} branch={branch} />
              ))}
            </ul>
          </>
        )}

        {plan.unreadable.length > 0 && (
          <>
            <SectionLabel className="mt-6">Could not be read from {dialog.tier}</SectionLabel>
            <ul className="space-y-2">
              {plan.unreadable.map((cell) => (
                <li key={addressKey(cell.at)} className="flex flex-wrap items-center gap-2">
                  <Where at={cell.at} />
                  <span className="inline-flex items-center gap-1 text-xs text-destructive">
                    <WarningIcon className="size-3.5" />
                    {cell.error}
                  </span>
                </li>
              ))}
            </ul>
          </>
        )}

        {plan.skipped.length > 0 && (
          <>
            <SectionLabel className="mt-6">Left alone</SectionLabel>
            <ul className="space-y-2">
              {plan.skipped.map((skip, index) => (
                <li key={index} className="flex flex-wrap items-center gap-2">
                  <span className="font-mono text-xs">{skip.key}</span>
                  <span className="text-xs text-muted-foreground">{skip.reason}</span>
                </li>
              ))}
            </ul>
          </>
        )}

        {dialog.error && <p className="mt-4 text-destructive">{dialog.error}</p>}
      </div>
      <SheetFooter className="flex-row items-center border-t border-border p-4">
        <span className="font-mono text-xs text-muted-foreground">{plural(count, "value")} chosen</span>
        <span className="flex-1" />
        <Button variant="outline" size="sm" disabled={busy} onClick={closeCopy}>
          Cancel
        </Button>
        <Button
          size="sm"
          data-action="confirm-copy"
          disabled={busy || count === 0}
          onClick={() => void confirmCopy()}
        >
          <CopyIcon />
          {busy ? "Copying…" : `Copy ${plural(count, "value")}`}
        </Button>
      </SheetFooter>
    </SheetContent>
  );
}

function Branch({ branch }: { branch: CopyBranch }) {
  const dialog = useValue(copying)!;
  const open = dialog.open.has(branch.folder);
  const enabled = branch.cells.filter((cell) => !cell.hereSet || dialog.overwriting);
  const picked = enabled.filter((cell) => dialog.chosen.has(addressKey(cell.at)));
  const all = enabled.length > 0 && picked.length === enabled.length;
  return (
    <li className="border-b border-border last:border-b-0">
      <div className="flex items-center gap-2 py-2">
        <button
          type="button"
          className="inline-flex size-6 items-center justify-center text-muted-foreground"
          aria-expanded={open}
          aria-label={`${open ? "collapse" : "expand"} ${folderName(branch.folder)}`}
          onClick={() => toggleBranch(branch.folder)}
        >
          <CaretRightIcon className={cn("size-3.5 transition-transform", open && "rotate-90")} weight="bold" />
        </button>
        <label className="inline-flex cursor-pointer items-center gap-2">
          <Checkbox
            checked={all}
            disabled={dialog.busy || enabled.length === 0}
            onCheckedChange={(on) =>
              toggleCopy(
                enabled.map((cell) => cell.at),
                on,
              )
            }
          />
          {branch.folder === "" ? (
            <HouseIcon className="size-4 text-muted-foreground" />
          ) : (
            <FolderIcon className="size-4 text-muted-foreground" />
          )}
          <span className="font-mono text-xs">
            {branch.folder === "" ? "/" : branch.folder.slice(1)}
          </span>
        </label>
        <span className="text-xs text-muted-foreground">{plural(branch.cells.length, "value")}</span>
      </div>
      {open && (
        <ul className="mb-2 ml-8 space-y-1.5">
          {branch.cells.map((cell) => (
            <Leaf key={addressKey(cell.at)} cell={cell} />
          ))}
        </ul>
      )}
    </li>
  );
}

function Leaf({ cell }: { cell: CopyCell }) {
  const dialog = useValue(copying)!;
  const key = addressKey(cell.at);
  const locked = cell.hereSet && !dialog.overwriting;
  return (
    <li className={cn("flex flex-col gap-1", locked && "opacity-50")}>
      <label className="inline-flex cursor-pointer flex-wrap items-center gap-2">
        <Checkbox
          checked={dialog.chosen.has(key)}
          disabled={dialog.busy || locked}
          onCheckedChange={() => toggleCopy([cell.at])}
        />
        {cell.class === "secret" ? (
          <LockIcon className="size-4 text-muted-foreground" />
        ) : (
          <KeyIcon className="size-4 text-muted-foreground" />
        )}
        <span className="font-mono text-xs">{cell.at.key}</span>
        {cell.at.environment !== "" && (
          <Chip>
            <StackIcon />
            {cell.at.environment}
          </Chip>
        )}
        {cell.hereSet && <Chip tone="muted">already set</Chip>}
      </label>
      <span className="grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-center gap-2 pl-6.5 font-mono text-xs">
        <Here cell={cell} />
        <span className="text-dim" aria-hidden="true">
          →
        </span>
        <There cell={cell} />
      </span>
    </li>
  );
}

function Where({ at }: { at: Address }) {
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      <span className="font-mono text-xs">{at.key}</span>
      <Chip>
        {at.folder === "" ? <HouseIcon /> : <FolderIcon />}
        {folderName(at.folder)}
      </Chip>
      {at.environment !== "" && (
        <Chip>
          <StackIcon />
          {at.environment}
        </Chip>
      )}
    </span>
  );
}

function Here({ cell }: { cell: CopyCell }) {
  const revealed = useValue(baselines).get(addressKey(cell.at));
  if (!cell.hereSet) return <span className="text-muted-foreground">empty</span>;
  if (cell.class === "secret") {
    return <span className="text-held">secret · v{cell.hereVersion}</span>;
  }
  return (
    <span className="truncate text-held" title={`v${cell.hereVersion}`}>
      {revealed ?? "••••••••"}
    </span>
  );
}

function There({ cell }: { cell: CopyCell }) {
  if (cell.class === "secret") {
    return <span className="text-muted-foreground">secret, copied without showing it</span>;
  }
  return <span className="truncate">{cell.there}</span>;
}
