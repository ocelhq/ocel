import {
  AppWindowIcon,
  FolderIcon,
  HouseIcon,
  LinkIcon,
  StackIcon,
  WarningIcon,
} from "@phosphor-icons/react";

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
        <SheetContent data-slot="drawer" className="gap-0 sm:max-w-md">
          <SheetHeader className="border-b border-border p-6">
            <SheetTitle className="font-mono text-base normal-case tracking-tight">
              {row.key}
            </SheetTitle>
            <SheetDescription className="sr-only">Details and history of {row.key}</SheetDescription>
            <Where variant={variant} />
          </SheetHeader>
          <div className="flex-1 overflow-y-auto p-6 text-sm">
            <Facts row={row} variant={variant} />
            {variant.problem && (
              <p className="mt-4 text-destructive">
                The value here fails its schema: {variant.problem}
              </p>
            )}
            {problem && (
              <p className="mt-4 text-destructive">
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
      <Chip>
        {folder === "" ? <HouseIcon /> : <FolderIcon />}
        {folderName(folder)}
      </Chip>
      <Chip>
        <StackIcon />
        {environment === "" ? "base" : environment}
      </Chip>
      {variant.orphaned && (
        <Chip tone="owed">
          <WarningIcon />
          orphaned
        </Chip>
      )}
    </div>
  );
}

function Facts({ row, variant }: { row: MatrixRow; variant: Variant }) {
  const apps = useValue(state)?.matrix.apps ?? [];
  const readers = readersOf(row, apps);
  const scope = row.scope ?? [];
  return (
    <dl className="grid grid-cols-[6rem_1fr] gap-x-4 gap-y-3 [&_dt]:font-mono [&_dt]:text-[11px] [&_dt]:tracking-[0.1em] [&_dt]:text-dim [&_dt]:uppercase">
      <dt>Class</dt>
      <dd className="font-mono text-xs">{row.class}</dd>
      <dt>Scope</dt>
      <dd>
        {scope.length === 0
          ? "every folder; the root value unless a folder sets its own"
          : `only ${names(scope)}`}
      </dd>
      <dt>Here</dt>
      <dd className="font-mono text-xs">
        {variant.set ? `set · v${variant.version}` : variant.owed ? "required, not set" : "not set"}
      </dd>
      {variant.reference && (
        <>
          <dt>Source</dt>
          <dd className="space-y-2">
            <Chip tone="ink">
              <LinkIcon />
              {referenceLine(variant.reference)}
            </Chip>
            <p className="text-xs text-muted-foreground">
              Live: edits there change what this project reads. Change the link with{" "}
              <code className="bg-chip px-1">ocel env ref {variant.at.key} …</code> in the terminal.
            </p>
          </dd>
        </>
      )}
      <dt>Read by</dt>
      <dd>
        {readers.length === 0 ? (
          <span className="text-muted-foreground">no app binds a folder that reads it</span>
        ) : (
          <span className="flex flex-wrap gap-1.5">
            {readers.map((app) => (
              <Chip key={app.name} title={`${app.name} reads ${folderName(app.folder)}, then root`}>
                <AppWindowIcon />
                {app.name}
              </Chip>
            ))}
          </span>
        )}
      </dd>
    </dl>
  );
}

function History() {
  const failed = useValue(historyError);
  const versions = useValue(history);
  if (failed) return <p className="text-destructive">Could not read the history: {failed}</p>;
  if (versions === null) return <p className="text-muted-foreground">Reading…</p>;
  if (versions.length === 0) {
    return <p className="text-muted-foreground">No versions stored yet; nothing has been saved here.</p>;
  }
  return (
    <ul className="divide-y divide-border border-y border-border font-mono text-xs">
      {versions.map((version) => (
        <li key={version.version} className="grid grid-cols-[3rem_1fr_auto] gap-3 py-2">
          <span className="font-medium">v{version.version}</span>
          <span className="text-muted-foreground">{whenLine(version.createdAt)}</span>
          <span className="text-dim">{sizeLine(version.size)}</span>
        </li>
      ))}
    </ul>
  );
}
