import {
  ArrowLeftIcon,
  ArrowRightIcon,
  CaretRightIcon,
  ClockCounterClockwiseIcon,
  DotsThreeIcon,
  DownloadIcon,
  EyeIcon,
  EyeSlashIcon,
  FolderIcon,
  KeyIcon,
  LockKeyIcon,
  TextTIcon,
  UploadSimpleIcon,
  WarningIcon,
  XIcon,
} from "@phosphor-icons/react";
import { type DragEvent, Fragment, useEffect, useRef, useState } from "react";
import { glyph, role } from "../lib/type";
import { cn } from "../lib/utils";
import {
  abilityOf,
  addressKey,
  type Bundle,
  baselineOf,
  type Class,
  editable,
  folderName,
  type Group,
  isDirty,
  type KeyLine,
  names,
  overrideOptions,
  plural,
  type Reference,
  referenceLine,
  revealable,
  setForOptions,
  type Variant,
  variableGroupTally,
} from "../model";
import { useValue } from "../signals";
import {
  ability,
  addOverride,
  applyDrop,
  askRemoval,
  baselines,
  bundleFolderKey,
  bundleOpen,
  bundleStates,
  catalogue,
  collapsed,
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
  importFile,
  listing,
  missingVariableGroupCells,
  openCopy,
  openDrawer,
  pickEnvironment,
  problems,
  reveal,
  revealErrors,
  revealGroup,
  saving,
  search,
  selected,
  selectVisible,
  setDraft,
  setFor,
  setSearch,
  shown,
  showUnfilled,
  spotlight,
  spotlighted,
  state,
  toggleBundle,
  toggleBundleFolder,
  toggleGroup,
  toggleRevealVisible,
  toggleSelected,
  unfilledLens as unfilledLensCount,
  unfilledOnly,
  visible,
} from "../store";
import { Chip, ChipButton } from "./Chip";
import { Fault } from "./Fault";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "./ui/dropdown-menu";
import { Input } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Switch } from "./ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "./ui/tooltip";

function hasFile(event: DragEvent): boolean {
  const types = event.dataTransfer?.types ?? [];
  return [...types].some((type) => type === "Files" || type === "text/plain");
}

function dropInto(event: DragEvent, into: string): void {
  if (!hasFile(event)) return;
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
  if (!hasFile(event)) return;
  event.preventDefault();
  event.stopPropagation();
  event.dataTransfer!.dropEffect = "copy";
  dragTarget.value = into;
}

const label = cn(role.label, "text-muted-foreground");
const datum = role.key;
const pathName = role.path;
const note = cn(role.meta, "text-muted-foreground");
const prose = cn(role.body, "text-body");
const rule = "border-r border-border";
const rowHeight = "h-10";
const cellKey = cn("w-[40%] py-0 pr-3 text-left align-middle font-normal", rule);
const cellValue = cn("w-[47%] p-0 align-middle", rule);
const cellTools = "w-[13%] min-w-28 px-1.5 text-right align-middle";
const indent = ["pl-4", "pl-11", "pl-18"] as const;
const gutter = "flex w-4 shrink-0 items-center justify-center";
const maskText = "********";
const noteIndent = ["pl-[4.125rem]", "pl-[5.875rem]", "pl-[7.625rem]"] as const;

const classMarks = {
  plain: { Icon: TextTIcon, tint: "text-dim" },
  sensitive: { Icon: KeyIcon, tint: "text-muted-foreground" },
  secret: { Icon: LockKeyIcon, tint: "text-foreground" },
} as const;

function ClassMark({ of }: { of: Class }) {
  const { Icon, tint } = classMarks[of];
  return (
    <Tooltip>
      <TooltipTrigger
        render={<span data-slot="class-mark" className="inline-flex shrink-0 items-center" />}
      >
        <Icon className={cn(glyph.inline, tint)} />
        <span className="sr-only">{of}</span>
      </TooltipTrigger>
      <TooltipContent>{of}</TooltipContent>
    </Tooltip>
  );
}

export function Table() {
  const current = useValue(state)!;
  const list = useValue(listing);
  const target = useValue(dragTarget);
  const open = useValue(expanded);
  const shownRows = useValue(visible);
  const picked = useValue(selected);
  const revealed = useValue(shown);
  const can = useValue(ability);
  const unfilledLens = useValue(unfilledOnly);
  const total =
    list.keys.length +
    list.bundles.reduce((sum, bundle) => sum + bundle.keys, 0) +
    list.groups.reduce((sum, group) => sum + group.keys, 0);
  return (
    <TooltipProvider>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: the card is a drop target; every action in it is a button */}
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
          <table className="w-full min-w-2xl table-fixed border-collapse text-sm [&>tbody:last-of-type>tr:last-child]:border-b-0">
            <thead>
              <tr className="h-9 border-b border-border">
                <th scope="col" className={cn(cellKey, indent[0], label)}>
                  <span className="inline-flex items-center gap-2.5">
                    <span className={gutter}>
                      <Checkbox
                        aria-label="select every row"
                        checked={shownRows.length > 0 && picked.size === shownRows.length}
                        disabled={shownRows.length === 0}
                        onCheckedChange={(on) => selectVisible(on)}
                      />
                    </span>
                    <span className={glyph.inline} aria-hidden />
                    Name
                  </span>
                </th>
                <th scope="col" className={cn(cellValue, "px-3 text-left", label)}>
                  Value
                </th>
                <th scope="col" className={cellTools}>
                  {can.reveal && (
                    <Button
                      variant="ghost"
                      size="xs"
                      className={cn(role.meta, "text-foreground")}
                      data-action="reveal-all"
                      aria-pressed={revealed}
                      disabled={shownRows.filter(revealable).length === 0}
                      onClick={toggleRevealVisible}
                    >
                      {revealed ? "Hide all" : "Reveal all"}
                    </Button>
                  )}
                </th>
              </tr>
            </thead>
            {list.groups.map((group) => (
              <FolderGroup group={group} open={open.has(group.folder)} key={group.folder} />
            ))}
            <tbody>
              {list.keys.map((line) => (
                <KeyRow line={line} flat={list.flat} depth={0} key={addressKey(line.variant.at)} />
              ))}
            </tbody>
            {list.bundles.map((bundle) => (
              <BundleSection bundle={bundle} key={bundle.group.key} />
            ))}
          </table>
        </div>
        {total === 0 && <Empty />}
        <footer
          className={cn(
            note,
            "flex flex-wrap items-center gap-x-6 gap-y-2 border-t border-border px-4 py-2.5",
          )}
        >
          <span className="inline-flex items-center gap-3">
            {!list.flat && <span>{plural(list.groups.length, "folder")}</span>}
            <span>{plural(total, "key")}</span>
          </span>
          <span>Drop a .env file to fill values</span>
        </footer>
        {target !== null && (
          <div
            data-slot="dropzone"
            aria-hidden="true"
            className="pointer-events-none absolute inset-0 z-10 flex flex-col items-center justify-center gap-1 border border-electric bg-background text-center"
          >
            <DownloadIcon className={cn(glyph.figure, "text-electric")} />
            <p className={cn(role.body, "text-foreground")}>
              Drop to fill {folderName(target)} values
            </p>
            <p className={cn(prose, "max-w-md")}>
              Fills as drafts; nothing is written until you save
            </p>
          </div>
        )}
        {current.recovery && total === 0 && unfilledLens && (
          <p className={cn(prose, "px-4 py-6")}>
            Every cell the deploy needs is filled. Save and resume below.
          </p>
        )}
      </section>
    </TooltipProvider>
  );
}

function BundleSection({ bundle }: { bundle: Bundle }) {
  const current = useValue(state);
  const unknown = current?.values === "unknown";
  const shut = unknown || !abilityOf(current).write;
  const states = useValue(bundleStates);
  const on = bundleOpen(states, bundle);
  const shutFolders = useValue(collapsed);
  const { members, present, missing } = variableGroupTally(states.get(bundle.group.key) ?? []);
  const total = members === 0 ? bundle.keys : members;
  return (
    <tbody data-slot="bundle" data-group={bundle.group.key} data-open={on}>
      <tr className="border-b border-border">
        <th
          scope="rowgroup"
          colSpan={3}
          className={cn("py-2.5 pr-4 text-left font-normal", indent[0])}
        >
          <span className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
            <Switch
              data-slot="group-switch"
              checked={on}
              disabled={shut}
              title={
                unknown
                  ? "the console cannot read these values"
                  : shut
                    ? "your role cannot change these values"
                    : undefined
              }
              aria-label={`${on ? "switch off" : "switch on"} ${bundle.group.key}`}
              onCheckedChange={(next) => toggleBundle(bundle.group.key, next)}
            />
            <span className={pathName}>{bundle.group.key}</span>
            {bundle.group.description && (
              <span className={cn(note, "min-w-0 truncate")}>{bundle.group.description}</span>
            )}
            <span className="flex-1" />
            <span
              data-slot="group-summary"
              className={cn(
                role.meta,
                "shrink-0 tabular-nums",
                on && missing > 0 ? "text-warn" : "text-muted-foreground",
              )}
            >
              {unknown
                ? "unknown"
                : !on
                  ? `off · ${plural(total, "key")}`
                  : missing > 0
                    ? `${plural(missing, "key")} to fill`
                    : `${present} of ${total} set`}
            </span>
          </span>
        </th>
      </tr>
      {on &&
        bundle.root.map((line) => (
          <KeyRow line={line} flat={false} depth={1} key={addressKey(line.variant.at)} />
        ))}
      {on &&
        bundle.folders.map((within) => {
          const shut = shutFolders.has(bundleFolderKey(bundle.group.key, within.folder));
          return (
            <Fragment key={within.folder}>
              <tr
                data-slot="bundle-folder"
                className="cursor-pointer border-b border-border hover:bg-muted/40"
                onClick={() => toggleBundleFolder(bundle.group.key, within.folder)}
              >
                <th
                  scope="rowgroup"
                  colSpan={3}
                  className={cn("py-2 pr-4 text-left font-normal", indent[1])}
                >
                  <span className="flex min-w-0 items-center gap-2.5">
                    <button
                      type="button"
                      aria-expanded={!shut}
                      aria-label={`${shut ? "expand" : "collapse"} ${within.folder} in ${bundle.group.key}`}
                      className="-m-1 inline-flex size-6 shrink-0 items-center justify-center p-1 text-muted-foreground"
                      onClick={(event) => {
                        event.stopPropagation();
                        toggleBundleFolder(bundle.group.key, within.folder);
                      }}
                    >
                      <CaretRightIcon
                        className={cn(glyph.control, "transition-transform", !shut && "rotate-90")}
                      />
                    </button>
                    <FolderIcon
                      weight="fill"
                      className={cn(glyph.control, "shrink-0 text-amber")}
                    />
                    <span className={pathName}>{within.folder}</span>
                    <span className={note}>{plural(within.lines.length, "key")}</span>
                  </span>
                </th>
              </tr>
              {!shut &&
                within.lines.map((line) => (
                  <KeyRow line={line} flat={false} depth={2} key={addressKey(line.variant.at)} />
                ))}
            </Fragment>
          );
        })}
    </tbody>
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
  const unfilledLens = useValue(unfilledOnly);
  const loading = useValue(copyLoading);
  const can = useValue(ability);
  const unfilledCount = useValue(unfilledLensCount);
  const folders = current.matrix.columns.filter((folder) => folder !== "");
  const items = [
    { value: "", label: "every environment" },
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
          aria-label="which environment these values apply to"
          data-action="environment"
          className={cn(role.path, "h-8")}
        >
          <span className={cn(label, "shrink-0")}>values for</span>
          <SelectValue />
        </SelectTrigger>
        <SelectContent align="start" alignItemWithTrigger={false}>
          {items.map((item) => (
            <SelectItem value={item.value} key={item.value}>
              {item.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {unfilledCount > 0 && (
        <Button
          variant={unfilledLens ? "secondary" : "ghost"}
          size="sm"
          data-action="unfilled-only"
          aria-pressed={unfilledLens}
          onClick={() => showUnfilled(!unfilledLens)}
        >
          <WarningIcon />
          {unfilledLens ? "Showing" : "Show"} {unfilledCount} to fill
        </Button>
      )}
      {!unfilledLens && list.flat && <Chip tone="muted">every folder</Chip>}
      <span className="flex-1" />
      <Input
        type="search"
        data-action="search"
        aria-label="search by name"
        placeholder="Search by name"
        className="h-8 w-56"
        value={query}
        onChange={(event) => setSearch(event.currentTarget.value)}
      />
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
      {can.write && (
        <DropdownMenu>
          <DropdownMenuTrigger render={<Button variant="outline" size="xs" data-action="import" />}>
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
                  {folder}
                </DropdownMenuItem>
              ))}
            </DropdownMenuGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      )}
      {can.write && can.reveal && (
        <Button
          variant="outline"
          size="xs"
          data-action="copy-other"
          disabled={loading}
          onClick={() => void openCopy()}
        >
          <ArrowLeftIcon />
          {loading ? "Reading…" : `Copy from ${current.other}`}
        </Button>
      )}
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
        No variables declared. Keys come from <code>defineEnv</code> in app code.
      </>
    ) : (
      <>Nothing reads a variable here.</>
    );
  return <p className={cn(prose, "px-4 py-6")}>{text}</p>;
}

function FolderGroup({ group, open }: { group: Group; open: boolean }) {
  const lit = useValue(spotlight);
  const readers = useValue(state)!
    .matrix.apps.filter((app) => app.folder === group.folder)
    .map((app) => app.name);
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
        className="cursor-pointer border-b border-border hover:bg-muted/40"
        onClick={() => toggleGroup(group.folder)}
      >
        <th
          scope="rowgroup"
          className={cn("py-2.5 pr-4 text-left font-normal", indent[0])}
          colSpan={3}
        >
          <span className="flex min-w-0 flex-wrap items-center gap-2.5">
            <button
              type="button"
              aria-expanded={open}
              aria-label={`${open ? "collapse" : "expand"} ${group.folder}`}
              className="-m-1 inline-flex size-6 shrink-0 items-center justify-center p-1 text-muted-foreground"
              onClick={(event) => {
                event.stopPropagation();
                toggleGroup(group.folder);
              }}
            >
              <CaretRightIcon
                className={cn(glyph.control, "transition-transform", open && "rotate-90")}
              />
            </button>
            <FolderIcon weight="fill" className={cn(glyph.control, "shrink-0 text-amber")} />
            <span className={pathName}>{group.folder}</span>
            <span className={note}>{plural(group.keys, "key")}</span>
            {readers.length > 0 && <span className={note}>read by {names(readers)}</span>}
            {group.unfilled > 0 && (
              <Chip tone="warn" data-slot="unfilled-count">
                {group.unfilled} to fill
              </Chip>
            )}
          </span>
        </th>
      </tr>
      {open &&
        group.lines.map((line) => (
          <KeyRow line={line} flat={false} depth={1} key={addressKey(line.variant.at)} />
        ))}
      {open && group.keys === 0 && (
        <tr className="border-b border-border">
          <td colSpan={3} className={cn(prose, "py-3 pr-4", indent[1])}>
            Nothing set here — every key inherits the root.
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

function KeyRow({ line, flat, depth }: { line: KeyLine; flat: boolean; depth: number }) {
  const { row, variant } = line;
  const key = addressKey(variant.at);
  const picked = useValue(selected).has(key);
  const groupMissing = useValue(missingVariableGroupCells).has(key);
  const open = editable(variant);
  const missing = variant.missing || line.needed || groupMissing;
  return (
    <>
      <tr
        data-slot="key-row"
        data-selected={picked}
        className={cn(
          rowHeight,
          "border-b border-border transition-colors hover:bg-muted/40",
          picked && "bg-muted",
        )}
      >
        <th scope="row" className={cn(cellKey, indent[depth] ?? indent[2])}>
          <div className="flex min-w-0 items-center gap-2.5">
            <span className={gutter}>
              {open && (
                <Checkbox
                  aria-label={`select ${describe(variant)}`}
                  checked={picked}
                  onCheckedChange={() => toggleSelected(variant.at)}
                />
              )}
            </span>
            <ClassMark of={row.class} />
            <span className={cn(datum, "min-w-0 shrink truncate")}>{row.key}</span>
            {row.description && (
              <span
                data-slot="description"
                title={row.description}
                className={cn(note, "min-w-0 shrink-[4] truncate font-normal")}
              >
                {row.description}
              </span>
            )}
            <span className="flex shrink-0 items-center gap-2">
              {flat && (
                <ChipButton onClick={() => revealGroup(variant.at.folder)}>
                  {folderName(variant.at.folder)}
                </ChipButton>
              )}
              {missing && (
                <Chip tone="warn" data-slot="missing">
                  {line.needed ? "deploy needs this" : "required"}
                </Chip>
              )}
              {row.scope && row.scope.length > 0 && (
                <Chip tone="muted" title={`only ${names(row.scope)} read it`}>
                  scoped
                </Chip>
              )}
            </span>
          </div>
        </th>
        <td className={cellValue}>
          {open ? (
            <Value line={line} />
          ) : (
            <span className={cn(prose, rowHeight, "flex items-center gap-1.5 px-3")}>
              only in
              {(row.scope ?? []).map((where) => (
                <ChipButton key={where} data-slot="pointer" onClick={() => revealGroup(where)}>
                  {where}
                </ChipButton>
              ))}
            </span>
          )}
        </td>
        <td className={cellTools}>{open && <Actions line={line} />}</td>
      </tr>
      <Notes line={line} depth={depth} />
    </>
  );
}

function Notes({ line, depth }: { line: KeyLine; depth: number }) {
  const { variant } = line;
  const key = addressKey(variant.at);
  const trouble = useValue(problems).get(key);
  const unreadable = useValue(revealErrors).get(key);
  const revealed = useValue(baselines);
  if (!trouble && !unreadable && !variant.orphaned && !variant.problem) return null;
  return (
    <tr data-slot="key-notes" className="border-b border-border">
      <td colSpan={3} className={cn("py-2 pr-4", noteIndent[depth] ?? noteIndent[2])}>
        <div className="flex flex-col gap-1">
          {trouble && (
            <Fault>
              {trouble.kind === "conflict"
                ? `Changed underneath you — now ${stored(variant, revealed.get(key))}. Nothing was written.`
                : trouble.message}
            </Fault>
          )}
          {unreadable && <Fault>could not reveal: {unreadable}</Fault>}
          {variant.orphaned && (
            <span className={cn(role.meta, "text-body")}>
              {variant.at.environment} no longer exists — nothing reads it
            </span>
          )}
          {variant.problem && <Fault>fails its schema: {variant.problem}</Fault>}
        </div>
      </td>
    </tr>
  );
}

function Value({ line }: { line: KeyLine }) {
  const { variant } = line;
  const key = addressKey(variant.at);
  const known = useValue(baselines);
  const typed = useValue(drafts);
  const trouble = useValue(problems);
  const wanted = useValue(focusing);
  const can = useValue(ability);
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
  const concealing = variant.set && !revealed;
  const stamp = variant.unknown
    ? "the console cannot read this value"
    : variant.set
      ? `set · v${variant.version}`
      : "not set";
  const marks = [
    dirty && (
      <Chip tone="accent" data-slot="unsaved" key="unsaved">
        unsaved
      </Chip>
    ),
    problem && (
      <Chip tone="bad" key="problem">
        {problem.kind === "conflict" ? "conflict" : "failed"}
      </Chip>
    ),
    !dirty && line.inherits === "root" && (
      <Chip tone="muted" key="inherits">
        inherits root
      </Chip>
    ),
    !dirty && line.inherits === "base" && (
      <Chip tone="muted" key="inherits">
        inherits base
      </Chip>
    ),
    variant.kind === "environment" && variant.set && !variant.orphaned && (
      <Chip key="override">override</Chip>
    ),
    variant.orphaned && (
      <Chip tone="warn" key="orphaned">
        orphaned
      </Chip>
    ),
    variant.at.environment === "" && line.overrides.length > 0 && (
      <Chip
        key="overrides"
        tone={line.orphaned ? "warn" : "default"}
        title={`overridden in ${names(line.overrides)}${line.orphaned ? "; an override names an environment that no longer exists" : ""}`}
      >
        {line.overrides.length === 1
          ? line.overrides[0]
          : plural(line.overrides.length, "override")}
      </Chip>
    ),
  ].filter(Boolean);
  return (
    <div className={cn(rowHeight, "flex items-stretch")}>
      <Input
        ref={input}
        type="text"
        data-slot="value-input"
        data-dirty={dirty}
        className={cn(
          role.mono,
          "h-full min-w-0 flex-1 rounded-none border-0 bg-transparent px-3 placeholder:font-sans hover:bg-foreground/5 focus-visible:ring-2 focus-visible:ring-ring/60 focus-visible:ring-inset",
          concealing && "placeholder:font-mono placeholder:text-foreground",
          dirty && "bg-electric/5 text-foreground",
        )}
        value={draft}
        autoComplete="off"
        spellCheck={false}
        disabled={variant.orphaned || variant.unknown || !can.write}
        title={can.write ? stamp : `${stamp} · your role cannot change it`}
        placeholder={
          variant.unknown
            ? "unknown"
            : !variant.set
              ? line.inherits === "root"
                ? "inherits the root value"
                : line.inherits === "base"
                  ? "inherits the base value"
                  : "not set"
              : revealed
                ? ""
                : maskText
        }
        aria-label={`value of ${describe(variant)}`}
        onChange={(event) => setDraft(variant.at, event.currentTarget.value)}
      />
      {marks.length > 0 && (
        <span className="flex max-w-1/2 shrink items-center gap-1 overflow-hidden pr-2 pl-1">
          {marks}
        </span>
      )}
    </div>
  );
}

function Linked({ variant, reference }: { variant: Variant; reference: Reference }) {
  const key = addressKey(variant.at);
  const value = useValue(baselines).get(key);
  return (
    <div className={cn(rowHeight, "flex flex-wrap items-center gap-1.5 px-3")}>
      <Chip tone="ink" title={`set · v${variant.version}`}>
        <ArrowRightIcon />
        reads {referenceLine(reference)}
      </Chip>
      {value !== undefined ? (
        <span className={cn(role.mono, "text-stored")}>{value}</span>
      ) : (
        <span className={cn(role.mono, "text-muted-foreground")}>{maskText}</span>
      )}
    </div>
  );
}

function stored(variant: Variant, value: string | undefined): string {
  if (variant.class === "secret") return `v${variant.version} (secrets stay out of the browser)`;
  if (value === undefined) return `v${variant.version}`;
  return `v${variant.version}: ${value}`;
}

function Actions({ line }: { line: KeyLine }) {
  const { row, variant } = line;
  const current = useValue(state)!;
  const busy = useValue(saving);
  const revealed = useValue(baselines).has(addressKey(variant.at));
  const known = useValue(catalogue);
  const can = useValue(ability);
  const env = variant.at.environment;
  const removal = variant.orphaned
    ? `Remove the ${env} override — ${env} no longer exists`
    : env === ""
      ? "Remove value"
      : `Remove the ${env} override`;
  const overrides = can.write ? overrideOptions(current, known, variant.at) : [];
  const folders = can.write && variant.at.folder === "" ? setForOptions(known, row) : [];
  const showValue = can.reveal && revealable(variant);
  const removable = can.write && variant.set && !variant.reference;
  return (
    <span className="inline-flex items-center justify-end gap-1">
      {showValue && (
        <Button
          variant="ghost"
          size="icon-sm"
          data-action="reveal"
          aria-pressed={revealed}
          title={revealed ? "Hide the value" : "Reveal the value"}
          aria-label={`${revealed ? "hide" : "reveal"} the value of ${describe(variant)}`}
          onClick={() => (revealed ? hide([variant.at]) : void reveal([variant.at]))}
        >
          {revealed ? <EyeSlashIcon weight="bold" /> : <EyeIcon weight="bold" />}
        </Button>
      )}
      <Button
        variant="ghost"
        size="icon-sm"
        data-action="details"
        aria-haspopup="dialog"
        title="Details and history"
        aria-label={`details and history of ${describe(variant)}`}
        onClick={() => openDrawer(variant.at)}
      >
        <ClockCounterClockwiseIcon weight="bold" />
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              variant="ghost"
              size="icon-sm"
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
          {(overrides.length > 0 || folders.length > 0 || showValue || variant.extra) && (
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
                    Set for {folder}
                  </DropdownMenuItem>
                ))}
                {overrides.map((name) => (
                  <DropdownMenuItem
                    key={name}
                    onClick={() => addOverride({ ...variant.at, environment: name })}
                  >
                    Override for {name}
                  </DropdownMenuItem>
                ))}
                {showValue && (
                  <DropdownMenuItem onClick={() => void copyValue(variant.at)}>
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
                <XIcon />
                {removal}
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
    </span>
  );
}
