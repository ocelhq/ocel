import type { ReactNode } from "react";

import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";

import {
  addressKey,
  folderName,
  names,
  readersOf,
  referenceLine,
  sizeLine,
  variantAt,
  whenLine,
  type MatrixRow,
  type Variant,
} from "../model";
import { useValue } from "../signals";
import { catalogue, closeDrawer, drawer, history, historyError, problems, state } from "../store";
import { Chip, SectionLabel } from "./Chip";

export function Drawer() {
  const at = useValue(drawer);
  const known = useValue(catalogue);
  const trouble = useValue(problems);
  const row = at && known.rows.find((candidate) => candidate.key === at.key);
  const variant = at && variantAt(known, at);
  const problem = at && trouble.get(addressKey(at));
  return (
    <Sheet open={at !== null} onOpenChange={(open) => !open && closeDrawer()}>
      {row && variant && (
        <SheetContent data-slot="drawer" className="gap-0 data-[side=right]:sm:max-w-md">
          <SheetHeader className="border-b border-border p-6 pr-14">
            <SheetTitle className="font-mono text-base">{row.key}</SheetTitle>
            <SheetDescription className="sr-only">Details and history of {row.key}</SheetDescription>
            <Where variant={variant} />
          </SheetHeader>
          <div className="flex-1 overflow-y-auto p-6 text-sm">
            <Facts row={row} variant={variant} />
            {variant.problem && (
              <p className="mt-4 font-mono text-xs text-destructive">
                △ The value here fails its schema: {variant.problem}
              </p>
            )}
            {problem && (
              <p className="mt-4 font-mono text-xs text-destructive">
                △{" "}
                {problem.kind === "conflict"
                  ? `This value changed since the page read it. Nothing was written here; decide again against what is there now: ${problem.message}`
                  : problem.message}
              </p>
            )}
            <SectionLabel className="mt-6">History</SectionLabel>
            <History />
          </div>
        </SheetContent>
      )}
    </Sheet>
  );
}

function Where({ variant }: { variant: Variant }) {
  const { folder, environment } = variant.at;
  return (
    <div className="flex flex-wrap gap-1.5">
      <Chip>{folderName(folder)}</Chip>
      <Chip>{environment === "" ? "base" : environment}</Chip>
      {variant.orphaned && <Chip tone="owed">orphaned</Chip>}
    </div>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[6rem_1fr] gap-x-4 border-b border-border px-4 py-2.5 last:border-b-0">
      <dt className="font-mono text-[13px] text-muted-foreground">{label}</dt>
      <dd className="font-sans text-[13.5px] text-body">{children}</dd>
    </div>
  );
}

function Facts({ row, variant }: { row: MatrixRow; variant: Variant }) {
  const apps = useValue(state)?.matrix.apps ?? [];
  const readers = readersOf(row, apps);
  const scope = row.scope ?? [];
  return (
    <dl className="border border-border bg-card">
      <Fact label="Class">
        <span className="font-mono text-foreground">{row.class}</span>
      </Fact>
      <Fact label="Scope">
        {scope.length === 0
          ? "every folder; the root value unless a folder sets its own"
          : `only ${names(scope)}`}
      </Fact>
      <Fact label="Here">
        <span className="font-mono text-foreground">
          {variant.set ? `set · v${variant.version}` : variant.owed ? "required, not set" : "not set"}
        </span>
      </Fact>
      {variant.reference && (
        <Fact label="Source">
          <div className="space-y-2">
            <Chip tone="ink">→ {referenceLine(variant.reference)}</Chip>
            <p className="text-xs">
              Live: edits there change what this project reads. Change the link with{" "}
              <code className="bg-chip px-1 text-foreground">ocel env ref {variant.at.key} …</code>{" "}
              in the terminal.
            </p>
          </div>
        </Fact>
      )}
      <Fact label="Read by">
        {readers.length === 0 ? (
          <span className="text-muted-foreground">no app binds a folder that reads it</span>
        ) : (
          <span className="flex flex-wrap gap-1.5">
            {readers.map((app) => (
              <Chip key={app.name} title={`${app.name} reads ${folderName(app.folder)}, then root`}>
                {app.name}
              </Chip>
            ))}
          </span>
        )}
      </Fact>
    </dl>
  );
}

function History() {
  const failed = useValue(historyError);
  const versions = useValue(history);
  if (failed) {
    return <p className="font-mono text-xs text-destructive">△ Could not read the history: {failed}</p>;
  }
  if (versions === null) return <p className="font-sans text-[13.5px] text-body">Reading…</p>;
  if (versions.length === 0) {
    return (
      <p className="font-sans text-[13.5px] text-body">
        No versions stored yet; nothing has been saved here.
      </p>
    );
  }
  return (
    <ul className="divide-y divide-border border border-border bg-card font-mono text-[13px]">
      {versions.map((version) => (
        <li key={version.version} className="grid grid-cols-[3rem_1fr_auto] gap-3 px-4 py-2.5">
          <span className="text-foreground">v{version.version}</span>
          <span className="text-body">{whenLine(version.createdAt)}</span>
          <span className="text-muted-foreground">{sizeLine(version.size)}</span>
        </li>
      ))}
    </ul>
  );
}
