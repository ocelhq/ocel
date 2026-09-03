import {
  CaretRightIcon,
  ClockCounterClockwiseIcon,
  CopyIcon,
  DotsThreeIcon,
  EyeIcon,
  EyeSlashIcon,
  FileTextIcon,
  FolderIcon,
  HouseIcon,
  KeyIcon,
  LinkIcon,
  LockIcon,
  MagnifyingGlassIcon,
  ShieldCheckIcon,
  StackIcon,
  TrashIcon,
  UploadSimpleIcon,
  WarningIcon,
  XIcon,
} from "@phosphor-icons/react";
import { useEffect, useRef, useState, type DragEvent } from "react";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { cn } from "@/lib/utils";

import {
  addressKey,
  baselineOf,
  editable,
  folderName,
  isDirty,
  names,
  overrideOptions,
  plural,
  readBy,
  referenceLine,
  revealable,
  setForOptions,
  type Group,
  type KeyLine,
  type Reference,
  type Variant,
} from "../model";
import { useValue } from "../signals";
import {
  addOverride,
  applyDrop,
  askRemoval,
  baselines,
  catalogue,
  copyLoading,
  copyValue,
  dismiss,
  drafts,
  dragTarget,
  environment,
  environments,
  expanded,
  focused,
  focusing,
  hide,
  hoveredApp,
  importFile,
  listing,
  openCopy,
  openDrawer,
  owedOnly,
  pickEnvironment,
  problems,
  reveal,
  revealErrors,
  revealGroup,
  saving,
  search,
  selectVisible,
  selected,
  setDraft,
  setFor,
  setSearch,
  shown,
  spotlight,
  spotlighted,
  state,
  toggleGroup,
  toggleRevealVisible,
  toggleSelected,
  visible,
} from "../store";
import { Chip, ChipButton } from "./Chip";

function carriesFile(event: DragEvent): boolean {
  const types = event.dataTransfer?.types ?? [];
  return [...types].some((type) => type === "Files" || type === "text/plain");
}

function dropInto(event: DragEvent, into: string): void {
  if (!carriesFile(event)) return;
  event.preventDefault();
  event.stopPropagation();
  const file = event.dataTransfer?.files[0];
  if (file) {
    importFile(file, into);
    return;
  }
  applyDrop("the dropped text", event.dataTransfer?.getData("text/plain") ?? "", into);
}

function aimAt(event: DragEvent, into: string): void {
  if (!carriesFile(event)) return;
  event.preventDefault();
  event.stopPropagation();
  event.dataTransfer!.dropEffect = "copy";
  dragTarget.value = into;
}

const cellPick = "w-10 pr-0 pl-4 align-middle";
const cellKey = "min-w-0 py-2.5 pr-3 pl-2 text-left align-middle font-normal";
const cellValue = "w-[46%] min-w-72 py-2 pr-3 align-middle";
const cellTools = "w-28 py-2 pr-3 pl-0 text-right align-middle";

export function Table() {
  const current = useValue(state)!;
  const list = useValue(listing);
  const target = useValue(dragTarget);
  const open = useValue(expanded);
  const shownRows = useValue(visible);
  const picked = useValue(selected);
  const revealed = useValue(shown);
  const owedLens = useValue(owedOnly);
  const total = list.keys.length + list.groups.reduce((sum, group) => sum + group.keys, 0);
  return (
    <section
      data-slot="card"
      className="relative border border-border bg-card"
      onDragOver={(event) => aimAt(event, "")}
      onDragLeave={(event) => {
        if (
          event.currentTarget instanceof Element &&
          event.relatedTarget instanceof Node &&
          event.currentTarget.contains(event.relatedTarget)
        ) {
          return;
        }
        dragTarget.value = null;
      }}
      onDrop={(event) => dropInto(event, "")}
    >
      <Toolbar />
      <div className="overflow-x-auto">
        <table className="w-full table-fixed border-collapse text-sm">
          <thead>
            <tr className="border-b border-border">
              <th scope="col" className={cellPick}>
                <Checkbox
                  aria-label="select every row"
                  checked={shownRows.length > 0 && picked.size === shownRows.length}
                  disabled={shownRows.length === 0}
                  onCheckedChange={(on) => selectVisible(on)}
                />
              </th>
              <th
                scope="col"
                className={cn(cellKey, "font-mono text-[11px] tracking-[0.1em] text-dim uppercase")}
              >
                Name
              </th>
              <th
                scope="col"
                className={cn(cellValue, "font-mono text-[11px] tracking-[0.1em] text-dim uppercase")}
              >
                Value
              </th>
              <th scope="col" className={cellTools}>
                <Button
                  variant="ghost"
                  size="xs"
                  data-action="reveal-all"
                  aria-pressed={revealed}
                  disabled={shownRows.filter(revealable).length === 0}
                  onClick={toggleRevealVisible}
                >
                  {revealed ? <EyeSlashIcon /> : <EyeIcon />}
                  {revealed ? "Hide" : "Reveal"}
                </Button>
              </th>
            </tr>
          </thead>
          <tbody>
            {list.keys.map((line) => (
              <KeyRow line={line} flat={list.flat} key={addressKey(line.variant.at)} />
            ))}
          </tbody>
          {list.groups.map((group) => (
            <FolderGroup group={group} open={open.has(group.folder)} key={group.folder} />
          ))}
        </table>
      </div>
      {total === 0 && <Empty />}
      <footer className="flex flex-wrap items-center gap-x-6 gap-y-2 border-t border-border px-4 py-2.5 font-mono text-[11px] text-dim">
        <span className="inline-flex items-center gap-3">
          {!list.flat && (
            <span className="inline-flex items-center gap-1.5">
              <FolderIcon className="size-3.5" /> {list.groups.length}
            </span>
          )}
          <span className="inline-flex items-center gap-1.5">
            <KeyIcon className="size-3.5" /> {total}
          </span>
        </span>
        <span className="inline-flex items-center gap-1.5">
          <FileTextIcon className="size-3.5" />
          Drop a .env file here to fill root values, or on a folder to fill that folder
        </span>
      </footer>
      {target !== null && (
        <div
          data-slot="dropzone"
          aria-hidden="true"
          className="pointer-events-none absolute inset-0 z-10 flex flex-col items-center justify-center gap-1 border-2 border-primary bg-background/90 text-center"
        >
          <UploadSimpleIcon className="size-6 text-primary" />
          <p className="font-mono text-sm">Drop to fill {folderName(target)} values</p>
          <p className="max-w-md text-xs text-muted-foreground">
            Keys the project declares fill in as unsaved drafts; nothing is written until you save
          </p>
        </div>
      )}
      {current.recovery && total === 0 && owedLens && (
        <p className="px-4 py-6 text-sm text-muted-foreground">
          Every cell the deploy needs has a value. Save and resume below.
        </p>
      )}
    </section>
  );
}

function Toolbar() {
  const current = useValue(state)!;
  const picker = useRef<HTMLInputElement>(null);
  const [into, setInto] = useState("");
  const list = useValue(listing);
  const env = useValue(environment);
  const envs = useValue(environments);
  const query = useValue(search);
  const owedLens = useValue(owedOnly);
  const loading = useValue(copyLoading);
  const folders = current.matrix.columns.filter((folder) => folder !== "");
  const items = [
    { value: "", label: "Base" },
    ...envs.map((known) => ({
      value: known.name,
      label: known.orphaned ? `${known.name} · no longer exists` : known.name,
    })),
  ];
  return (
    <div
      data-slot="toolbar"
      className="flex flex-wrap items-center gap-2 border-b border-border px-4 py-2.5"
    >
      <Select value={env} items={items} onValueChange={(name) => pickEnvironment(name ?? "")}>
        <SelectTrigger
          size="sm"
          aria-label="environment"
          data-action="environment"
          className="h-8 border-border px-2.5 font-mono text-xs"
        >
          <StackIcon className="text-muted-foreground" />
          <SelectValue />
        </SelectTrigger>
        <SelectContent align="start" alignItemWithTrigger={false} className="font-mono text-xs">
          {items.map((item) => (
            <SelectItem value={item.value} key={item.value} className="text-xs">
              {item.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {owedLens && (
        <Chip tone="owed">
          <WarningIcon />
          {plural(list.keys.length, "cell")} the deploy needs
        </Chip>
      )}
      {!owedLens && list.flat && (
        <Chip tone="muted">
          <MagnifyingGlassIcon />
          results across every folder
        </Chip>
      )}
      <span className="flex-1" />
      <label className="relative">
        <MagnifyingGlassIcon className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input
          type="search"
          data-action="search"
          placeholder="Search by name"
          className="h-8 w-56 border-border pr-2 pl-8 font-mono text-xs"
          value={query}
          onChange={(event) => setSearch(event.currentTarget.value)}
        />
      </label>
      <input
        type="file"
        accept=".env,text/plain"
        className="sr-only"
        ref={picker}
        tabIndex={-1}
        onChange={(event) => {
          const file = event.currentTarget.files?.[0];
          event.currentTarget.value = "";
          if (file) importFile(file, into);
        }}
      />
      <DropdownMenu>
        <DropdownMenuTrigger
          render={<Button variant="outline" size="xs" data-action="import" />}
        >
          <UploadSimpleIcon />
          Import .env
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuGroup>
            <DropdownMenuLabel>Fill values in</DropdownMenuLabel>
            <DropdownMenuItem
              onClick={() => {
                setInto("");
                picker.current?.click();
              }}
            >
              <HouseIcon />
              root
            </DropdownMenuItem>
            {folders.map((folder) => (
              <DropdownMenuItem
                key={folder}
                onClick={() => {
                  setInto(folder);
                  picker.current?.click();
                }}
              >
                <FolderIcon />
                {folder.slice(1)}
              </DropdownMenuItem>
            ))}
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>
      <Button
        variant="outline"
        size="xs"
        data-action="copy-other"
        disabled={loading}
        onClick={() => void openCopy()}
      >
        <CopyIcon />
        {loading ? "Reading…" : `Copy from ${current.other}`}
      </Button>
    </div>
  );
}

function Empty() {
  const current = useValue(state)!;
  const query = useValue(search);
  const text =
    query.trim() !== "" ? (
      <>No variable is named like “{query.trim()}”.</>
    ) : current.matrix.rows.length === 0 ? (
      <>
        This project declares no variables yet. Keys come from <code>defineEnv</code> in app
        code; this page cannot create one.
      </>
    ) : (
      <>Nothing reads a variable here.</>
    );
  return <p className="px-4 py-6 text-sm text-muted-foreground">{text}</p>;
}

function FolderGroup({ group, open }: { group: Group; open: boolean }) {
  const lit = useValue(spotlight);
  const header = useRef<HTMLTableRowElement>(null);
  useEffect(() => {
    if (lit !== group.folder) return;
    header.current?.scrollIntoView({ block: "center", behavior: "smooth" });
    spotlighted();
  }, [lit, group.folder]);
  return (
    <tbody
      data-group={group.folder}
      data-open={open}
      onDragOver={(event) => aimAt(event, group.folder)}
      onDrop={(event) => dropInto(event, group.folder)}
    >
      <tr
        ref={header}
        data-slot="group-row"
        className="cursor-pointer border-t border-b border-border bg-muted/40 hover:bg-muted/70"
        onClick={() => toggleGroup(group.folder)}
      >
        <td className={cn(cellPick, "text-center")}>
          <button
            type="button"
            aria-expanded={open}
            aria-label={`${open ? "collapse" : "expand"} ${group.folder.slice(1)}`}
            className="inline-flex size-6 items-center justify-center text-muted-foreground"
            onClick={(event) => {
              event.stopPropagation();
              toggleGroup(group.folder);
            }}
          >
            <CaretRightIcon
              className={cn("size-3.5 transition-transform", open && "rotate-90")}
              weight="bold"
            />
          </button>
        </td>
        <th scope="rowgroup" className={cn(cellKey, "font-normal")} colSpan={3}>
          <span className="inline-flex flex-wrap items-center gap-2">
            <span className="inline-flex items-center gap-2 font-mono text-[13px] font-medium">
              <FolderIcon className="size-4 text-muted-foreground" />
              {group.folder.slice(1)}
            </span>
            <span className="font-mono text-[11px] text-dim">{plural(group.keys, "key")}</span>
            {group.owed > 0 && (
              <Chip tone="owed" data-slot="owed-count">
                {group.owed} to fill
              </Chip>
            )}
          </span>
        </th>
      </tr>
      {open &&
        group.lines.map((line) => (
          <KeyRow line={line} flat={false} key={addressKey(line.variant.at)} />
        ))}
      {open && group.lines.length === 0 && (
        <tr className="border-b border-border">
          <td />
          <td colSpan={3} className="py-3 pl-2 text-xs text-muted-foreground">
            Nothing is set for {group.folder.slice(1)}; every key it reads inherits the root.
            Use a root row’s menu, or drop a .env file here, to set one.
          </td>
        </tr>
      )}
    </tbody>
  );
}

function describe(variant: Variant): string {
  const { key, folder: where, environment: env } = variant.at;
  const place = where === "" ? "the root" : where;
  return env === "" ? `${key} in ${place}` : `${key} in ${place} for ${env}`;
}

function ClassIcon({ kind }: { kind: KeyLine["row"]["class"] }) {
  const className = "size-4 shrink-0 text-muted-foreground";
  if (kind === "secret") return <LockIcon className={className} />;
  if (kind === "sensitive") return <ShieldCheckIcon className={className} />;
  return <KeyIcon className={className} />;
}

function KeyRow({ line, flat }: { line: KeyLine; flat: boolean }) {
  const { row, variant } = line;
  const key = addressKey(variant.at);
  const app = useValue(hoveredApp);
  const picked = useValue(selected).has(key);
  const open = editable(variant);
  const dimmed = app !== null && !readBy(row, app);
  const owed = variant.owed || line.needed;
  return (
    <tr
      data-slot="key-row"
      data-selected={picked}
      className={cn(
        "border-b border-border transition-opacity last:border-b-0",
        picked && "bg-muted/60",
        dimmed && "opacity-40",
      )}
    >
      <td className={cellPick}>
        {open && (
          <Checkbox
            aria-label={`select ${describe(variant)}`}
            checked={picked}
            onCheckedChange={() => toggleSelected(variant.at)}
          />
        )}
      </td>
      <th scope="row" className={cellKey}>
        <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span className="inline-flex items-center gap-2 font-mono text-[13px]">
            <ClassIcon kind={row.class} />
            <span className="truncate">{row.key}</span>
          </span>
          {flat && (
            <ChipButton onClick={() => revealGroup(variant.at.folder)}>
              {variant.at.folder === "" ? <HouseIcon /> : <FolderIcon />}
              {folderName(variant.at.folder)}
            </ChipButton>
          )}
          {row.class !== "plain" && <Chip tone="muted">{row.class}</Chip>}
          {owed && (
            <Chip tone="owed" data-slot="owed">
              {line.needed ? "deploy needs this" : "required"}
            </Chip>
          )}
          {row.scope && row.scope.length > 0 && (
            <Chip tone="muted" title={`only ${names(row.scope)} read it`}>
              scoped
            </Chip>
          )}
        </span>
      </th>
      <td className={cellValue}>
        {open ? (
          <Value line={line} />
        ) : (
          <span className="inline-flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
            only in
            {(row.scope ?? []).map((where) => (
              <ChipButton key={where} data-slot="pointer" onClick={() => revealGroup(where)}>
                <FolderIcon />
                {where.slice(1)}
              </ChipButton>
            ))}
          </span>
        )}
      </td>
      <td className={cellTools}>{open && <Actions line={line} />}</td>
    </tr>
  );
}

function Fault({ children }: { children: React.ReactNode }) {
  return (
    <span className="inline-flex items-start gap-1 text-xs text-destructive">
      <WarningIcon className="mt-0.5 size-3.5 shrink-0" />
      <span>{children}</span>
    </span>
  );
}

function Value({ line }: { line: KeyLine }) {
  const { variant } = line;
  const key = addressKey(variant.at);
  const known = useValue(baselines);
  const typed = useValue(drafts);
  const trouble = useValue(problems);
  const unreadableAll = useValue(revealErrors);
  const wanted = useValue(focusing);
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (wanted !== key) return;
    input.current?.focus();
    focused();
  }, [wanted, key]);
  if (variant.reference) {
    return <Linked variant={variant} reference={variant.reference} />;
  }
  const revealed = known.has(key);
  const draft = typed.get(key) ?? baselineOf(variant.at, known);
  const dirty = isDirty(variant.at, typed, known);
  const problem = trouble.get(key);
  const unreadable = unreadableAll.get(key);
  const secret = variant.class === "secret";
  const stamp = variant.set ? `set · v${variant.version}` : "not set";
  return (
    <div className="flex flex-col gap-1">
      <div className="flex flex-wrap items-center gap-1.5">
        <Input
          ref={input}
          type="text"
          data-slot="value-input"
          data-dirty={dirty}
          className={cn(
            "h-8 min-w-40 flex-1 border-input px-2 font-mono text-xs",
            dirty && "border-primary focus-visible:border-primary",
          )}
          value={draft}
          autoComplete="off"
          spellCheck={false}
          disabled={variant.orphaned}
          title={stamp}
          placeholder={
            !variant.set
              ? line.inherits === "root"
                ? "inherits the root value"
                : line.inherits === "base"
                  ? "inherits the base value"
                  : "not set"
              : secret
                ? "overwrite the secret"
                : revealed
                  ? ""
                  : "••••••••"
          }
          aria-label={`value of ${describe(variant)}`}
          onChange={(event) => setDraft(variant.at, event.currentTarget.value)}
        />
        {dirty && (
          <Chip tone="accent" data-slot="unsaved">
            unsaved
          </Chip>
        )}
        {problem && <Chip tone="owed">{problem.kind === "conflict" ? "conflict" : "failed"}</Chip>}
        {!dirty && line.inherits === "root" && <Chip tone="muted">inherits root</Chip>}
        {!dirty && line.inherits === "base" && <Chip tone="muted">inherits base</Chip>}
        {variant.kind === "environment" && variant.set && !variant.orphaned && (
          <Chip>
            <StackIcon />
            override
          </Chip>
        )}
        {variant.orphaned && (
          <Chip tone="owed">
            <WarningIcon />
            orphaned
          </Chip>
        )}
        {variant.at.environment === "" && line.overrides.length > 0 && (
          <Chip
            tone={line.orphaned ? "owed" : "default"}
            title={`overridden in ${names(line.overrides)}${line.orphaned ? "; an override names an environment that no longer exists" : ""}`}
          >
            <StackIcon />
            {line.overrides.length === 1 ? line.overrides[0] : plural(line.overrides.length, "override")}
          </Chip>
        )}
      </div>
      {problem && (
        <Fault>
          {problem.kind === "conflict"
            ? `Changed underneath you — now ${stored(variant, revealed ? known.get(key) : undefined)}. Nothing was written; decide again.`
            : problem.message}
        </Fault>
      )}
      {unreadable && <Fault>could not reveal: {unreadable}</Fault>}
      {variant.orphaned && (
        <span className="text-xs text-muted-foreground">
          nothing reads it — {variant.at.environment} no longer exists
        </span>
      )}
      {variant.problem && <Fault>fails its schema: {variant.problem}</Fault>}
    </div>
  );
}

function Linked({ variant, reference }: { variant: Variant; reference: Reference }) {
  const key = addressKey(variant.at);
  const value = useValue(baselines).get(key);
  const unreadable = useValue(revealErrors).get(key);
  return (
    <div className="flex flex-col gap-1">
      <div className="flex flex-wrap items-center gap-1.5">
        <Chip tone="ink" title={`set · v${variant.version}`}>
          <LinkIcon />
          reads {referenceLine(reference)}
        </Chip>
        {value !== undefined ? (
          <span className="font-mono text-xs text-held">{value}</span>
        ) : (
          <span className="font-mono text-xs text-muted-foreground">••••••••</span>
        )}
      </div>
      {unreadable && <Fault>source unreadable: {unreadable}</Fault>}
    </div>
  );
}

function stored(variant: Variant, value: string | undefined): string {
  if (variant.class === "secret") return `v${variant.version} (a secret stays out of the browser)`;
  if (value === undefined) return `v${variant.version}`;
  return `v${variant.version}: ${value}`;
}

function Actions({ line }: { line: KeyLine }) {
  const { row, variant } = line;
  const current = useValue(state)!;
  const busy = useValue(saving);
  const revealed = useValue(baselines).has(addressKey(variant.at));
  const known = useValue(catalogue);
  const env = variant.at.environment;
  const removal = variant.orphaned
    ? `Remove the ${env} override — ${env} no longer exists`
    : env === ""
      ? "Remove value"
      : `Remove the ${env} override`;
  const overrides = overrideOptions(current, known, variant.at);
  const folders = variant.at.folder === "" ? setForOptions(known, row) : [];
  const removable = variant.set && !variant.reference;
  return (
    <span className="inline-flex items-center justify-end gap-0.5">
      {revealable(variant) && (
        <Button
          variant="ghost"
          size="icon-xs"
          data-action="reveal"
          aria-pressed={revealed}
          title={revealed ? "Hide the value" : "Reveal the value"}
          aria-label={`${revealed ? "hide" : "reveal"} the value of ${describe(variant)}`}
          onClick={() => (revealed ? hide([variant.at]) : void reveal([variant.at]))}
        >
          {revealed ? <EyeSlashIcon /> : <EyeIcon />}
        </Button>
      )}
      <Button
        variant="ghost"
        size="icon-xs"
        data-action="details"
        aria-haspopup="dialog"
        title="Details and history"
        aria-label={`details and history of ${describe(variant)}`}
        onClick={() => openDrawer(variant.at)}
      >
        <ClockCounterClockwiseIcon />
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              variant="ghost"
              size="icon-xs"
              data-action="menu"
              title="More"
              aria-label={`actions for ${describe(variant)}`}
            />
          }
        >
          <DotsThreeIcon weight="bold" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuGroup>
            <DropdownMenuLabel>Insights</DropdownMenuLabel>
            <DropdownMenuItem onClick={() => openDrawer(variant.at)}>
              <ClockCounterClockwiseIcon />
              Details and history
            </DropdownMenuItem>
          </DropdownMenuGroup>
          {(overrides.length > 0 || folders.length > 0 || revealable(variant) || variant.extra) && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuGroup>
                <DropdownMenuLabel>Manage</DropdownMenuLabel>
                {folders.map((folder) => (
                  <DropdownMenuItem
                    key={folder}
                    data-action="set-for"
                    onClick={() => setFor({ key: row.key, folder, environment: "" })}
                  >
                    <FolderIcon />
                    Set for {folder.slice(1)}
                  </DropdownMenuItem>
                ))}
                {overrides.map((name) => (
                  <DropdownMenuItem
                    key={name}
                    onClick={() => addOverride({ ...variant.at, environment: name })}
                  >
                    <StackIcon />
                    Override for {name}
                  </DropdownMenuItem>
                ))}
                {revealable(variant) && (
                  <DropdownMenuItem onClick={() => void copyValue(variant.at)}>
                    <CopyIcon />
                    Copy value
                  </DropdownMenuItem>
                )}
                {variant.extra && (
                  <DropdownMenuItem onClick={() => dismiss(variant.at)}>
                    <XIcon />
                    Dismiss this empty row
                  </DropdownMenuItem>
                )}
              </DropdownMenuGroup>
            </>
          )}
          {removable && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                variant="destructive"
                disabled={busy}
                onClick={() => askRemoval([variant.at])}
              >
                <TrashIcon />
                {removal}
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
    </span>
  );
}
