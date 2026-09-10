export type CellState = "required" | "optional" | "forbidden";

export type Class = "plain" | "sensitive" | "secret";

export interface Cell {
  key: string;
  folder: string;
}

export interface Address extends Cell {
  environment: string;
}

export interface Reference {
  slug: string;
  folder: string;
  key: string;
}

export interface Override {
  environment: string;
  version: number;
  orphaned?: boolean;
  reference?: Reference;
}

export interface MatrixCell {
  folder: string;
  state: CellState;
  set: boolean;
  version: number;
  overrides?: Override[];
  reference?: Reference;
  problem?: string;
}

export interface MatrixRow {
  key: string;
  description?: string;
  class: Class;
  scope?: string[];
  group?: string;
  cells: MatrixCell[];
}

export interface VariableGroup {
  key: string;
  required: boolean;
  description?: string;
}

export interface AppResolution {
  name: string;
  folder: string;
  missing?: Cell[];
}

export interface Recovery {
  deploy: string;
  owed: Cell[];
}

export interface State {
  slug: string;
  tier: string;
  other: string;
  environments: string[];
  matrix: {
    columns: string[];
    rows: MatrixRow[];
    groups?: VariableGroup[];
    apps: AppResolution[];
  };
  recovery?: Recovery;
}

export interface Version {
  version: number;
  createdAt: number;
  size: number;
}

export type VariantKind = "root" | "folder" | "environment";

export interface Variant {
  at: Address;
  kind: VariantKind;
  class: Class;
  state: CellState;
  set: boolean;
  version: number;
  orphaned: boolean;
  extra: boolean;
  owed: boolean;
  reference?: Reference;
  problem?: string;
}

export function addressKey(at: Address): string {
  return `${at.key} ${at.folder} ${at.environment}`;
}

export function overrideOf(cell: MatrixCell, environment: string): Override | undefined {
  return cell.overrides?.find((held) => held.environment === environment);
}

export function held(cell: MatrixCell, environment: string): { set: boolean; version: number } {
  if (environment === "") return { set: cell.set, version: cell.version };
  const override = overrideOf(cell, environment);
  return { set: override !== undefined, version: override?.version ?? 0 };
}

export function cellOf(row: MatrixRow, folder: string): MatrixCell | undefined {
  return row.cells.find((cell) => cell.folder === folder);
}

export function owedCell(row: MatrixRow, cell: MatrixCell, off: ReadonlySet<string>): boolean {
  const owing = cell.state === "required" && !cell.set && !offVariableGroup(row, cell.folder, off);
  return owing || cell.problem !== undefined;
}

export function owedCount(current: State): number {
  const off = offVariableGroupsOf(current);
  return current.matrix.rows.reduce(
    (total, row) => total + row.cells.filter((cell) => owedCell(row, cell, off)).length,
    0,
  );
}

export function readable(row: MatrixRow): string[] {
  return row.cells
    .filter((cell) => cell.state !== "forbidden" || cell.set)
    .map((cell) => cell.folder);
}

export function readBy(row: MatrixRow, app: AppResolution): boolean {
  const folders = readable(row);
  return folders.includes("") || folders.includes(app.folder);
}

export function readersOf(row: MatrixRow, apps: readonly AppResolution[]): AppResolution[] {
  return apps.filter((app) => readBy(row, app));
}

const forbiddenRoot: MatrixCell = {
  folder: "",
  state: "forbidden",
  set: false,
  version: 0,
};

export function variantOf(
  row: MatrixRow,
  cell: MatrixCell,
  environment: string,
  extra: boolean,
  off: ReadonlySet<string>,
): Variant {
  const override = environment === "" ? undefined : overrideOf(cell, environment);
  const reference = environment === "" ? cell.reference : override?.reference;
  const set = environment === "" ? cell.set || reference !== undefined : override !== undefined;
  return {
    at: { key: row.key, folder: cell.folder, environment },
    kind: environment !== "" ? "environment" : cell.folder === "" ? "root" : "folder",
    class: row.class,
    state: cell.state,
    set,
    version: environment === "" ? cell.version : (override?.version ?? 0),
    orphaned: override?.orphaned === true,
    extra,
    owed:
      environment === "" &&
      cell.state === "required" &&
      !set &&
      !offVariableGroup(row, cell.folder, off),
    ...(reference && { reference }),
    ...(environment === "" && cell.problem && { problem: cell.problem }),
  };
}

function materialised(cell: MatrixCell): boolean {
  return (
    cell.set ||
    cell.reference !== undefined ||
    cell.state === "required" ||
    cell.problem !== undefined ||
    (cell.overrides?.length ?? 0) > 0
  );
}

export interface Catalogue {
  rows: readonly MatrixRow[];
  variants: ReadonlyMap<string, Variant>;
  off: ReadonlySet<string>;
}

export function catalogueOf(current: State, extras: readonly Address[]): Catalogue {
  const off = offVariableGroupsOf(current);
  const variants = new Map<string, Variant>();
  const put = (variant: Variant) => {
    const key = addressKey(variant.at);
    if (!variants.has(key)) variants.set(key, variant);
  };
  for (const row of current.matrix.rows) {
    const asked = extras.filter((at) => at.key === row.key);
    const cells = cellOf(row, "") ? row.cells : [forbiddenRoot, ...row.cells];
    for (const cell of cells) {
      const real = cell.folder === "" || materialised(cell);
      const wanted = asked.some((at) => at.folder === cell.folder && at.environment === "");
      if (real || wanted) put(variantOf(row, cell, "", !real, off));
      for (const override of cell.overrides ?? []) {
        put(variantOf(row, cell, override.environment, false, off));
      }
      for (const at of asked) {
        if (at.folder === cell.folder && at.environment !== "") {
          put(variantOf(row, cell, at.environment, true, off));
        }
      }
    }
  }
  return { rows: current.matrix.rows, variants, off };
}

export function variantsOf(catalogue: Catalogue): Variant[] {
  return [...catalogue.variants.values()];
}

export function variantAt(catalogue: Catalogue, at: Address): Variant | undefined {
  const known = catalogue.variants.get(addressKey(at));
  if (known) return known;
  const row = catalogue.rows.find((candidate) => candidate.key === at.key);
  const cell = row && cellOf(row, at.folder);
  if (!row || !cell) return undefined;
  return variantOf(row, cell, at.environment, true, catalogue.off);
}

export type VariableGroupStatus = "off" | "partial" | "complete";

export interface VariableGroupMember {
  at: Address;
  addresses: Address[];
  required: boolean;
  present: boolean;
}

export interface VariableGroupState {
  group: VariableGroup;
  folder: string;
  environment: string;
  status: VariableGroupStatus;
  switchedOn: boolean;
  members: VariableGroupMember[];
  present: number;
  owed: VariableGroupMember[];
}

export interface VariableGroupPending {
  drafts: ReadonlyMap<string, string>;
  removals: ReadonlySet<string>;
  switchedOn: ReadonlySet<string>;
}

export function variableGroupsOf(current: State): VariableGroup[] {
  return current.matrix.groups ?? [];
}

export function variableGroupOf(
  current: State,
  key: string | undefined,
): VariableGroup | undefined {
  if (key === undefined || key === "") return undefined;
  return variableGroupsOf(current).find((group) => group.key === key);
}

export function variableGroupColumn(folder: string, environment: string): string {
  return `${folder} ${environment}`;
}

export function variableGroupColumnKey(group: string, folder: string, environment: string): string {
  return `${group} ${variableGroupColumn(folder, environment)}`;
}

function resolution(row: MatrixRow, folder: string): MatrixCell[] {
  const scope = row.scope ?? [];
  const chain =
    scope.length > 0
      ? scope.includes(folder)
        ? [folder]
        : []
      : folder === ""
        ? [""]
        : [folder, ""];
  const out: MatrixCell[] = [];
  for (const where of chain) {
    const found = cellOf(row, where);
    if (found && (found.state !== "forbidden" || found.set)) out.push(found);
  }
  return out;
}

function filled(cell: MatrixCell, at: Address, pending: VariableGroupPending): boolean {
  const key = addressKey(at);
  const draft = pending.drafts.get(key);
  if (draft !== undefined) return draft !== "";
  if (pending.removals.has(key)) return false;
  if (at.environment === "") return cell.set || cell.reference !== undefined;
  return overrideOf(cell, at.environment) !== undefined;
}

function memberOf(
  row: MatrixRow,
  folder: string,
  environment: string,
  pending: VariableGroupPending,
): VariableGroupMember | undefined {
  const cells = resolution(row, folder);
  const root = cells[cells.length - 1];
  if (root === undefined) return undefined;
  const reach: { cell: MatrixCell; at: Address }[] = [];
  for (const cell of cells) {
    if (environment !== "") {
      reach.push({ cell, at: { key: row.key, folder: cell.folder, environment } });
    }
    reach.push({ cell, at: { key: row.key, folder: cell.folder, environment: "" } });
  }
  return {
    at: reach[0]!.at,
    addresses: reach.map((step) => step.at),
    required: root.state === "required",
    present: reach.some((step) => filled(step.cell, step.at, pending)),
  };
}

export function variableGroupStateOf(
  current: State,
  key: string,
  folder: string,
  environment: string,
  pending: VariableGroupPending,
): VariableGroupState | undefined {
  const group = variableGroupOf(current, key);
  if (group === undefined) return undefined;
  const members = current.matrix.rows
    .filter((row) => row.group === key)
    .flatMap((row) => {
      const member = memberOf(row, folder, environment, pending);
      return member ? [member] : [];
    });
  if (members.length === 0) return undefined;
  const owed = members.filter((member) => member.required && !member.present);
  const present = members.filter((member) => member.present).length;
  return {
    group,
    folder,
    environment,
    status: present === 0 ? "off" : owed.length === 0 ? "complete" : "partial",
    switchedOn: pending.switchedOn.has(variableGroupColumnKey(key, folder, environment)),
    members,
    present,
    owed,
  };
}

const settled: VariableGroupPending = {
  drafts: new Map(),
  removals: new Set(),
  switchedOn: new Set(),
};

export function offVariableGroupsOf(current: State): ReadonlySet<string> {
  const out = new Set<string>();
  for (const derived of variableGroupStatesOf(current, "", settled)) {
    if (derived.status === "off" && !derived.group.required) {
      out.add(variableGroupColumnKey(derived.group.key, derived.folder, ""));
    }
  }
  return out;
}

function offVariableGroup(row: MatrixRow, folder: string, off: ReadonlySet<string>): boolean {
  return row.group !== undefined && off.has(variableGroupColumnKey(row.group, folder, ""));
}

export function variableGroupStatesOf(
  current: State,
  environment: string,
  pending: VariableGroupPending,
): VariableGroupState[] {
  const out: VariableGroupState[] = [];
  for (const group of variableGroupsOf(current)) {
    for (const folder of current.matrix.columns) {
      const derived = variableGroupStateOf(current, group.key, folder, environment, pending);
      if (derived) out.push(derived);
    }
  }
  return out;
}

export function variableGroupSwitchable(derived: VariableGroupState): boolean {
  return !derived.group.required;
}

export function owedVariableGroupCellsOf(
  states: readonly VariableGroupState[],
): ReadonlySet<string> {
  const out = new Set<string>();
  for (const derived of states) {
    if (derived.status !== "partial") continue;
    for (const member of derived.owed) out.add(addressKey(member.at));
  }
  return out;
}

export function blockedVariableGroupColumns(
  states: readonly VariableGroupState[],
): ReadonlySet<string> {
  return new Set(
    states
      .filter((derived) => derived.status === "partial")
      .map((derived) => variableGroupColumn(derived.folder, derived.environment)),
  );
}

export function variableGroupBlockLine(states: readonly VariableGroupState[]): string {
  const blocked = states.filter((derived) => derived.status === "partial");
  const line = names(
    blocked.map(
      (derived) =>
        `${derived.group.key} in ${folderName(derived.folder)}${
          derived.environment === "" ? "" : ` for ${derived.environment}`
        }`,
    ),
  );
  return blocked.some(variableGroupSwitchable)
    ? `${line} must be complete before saving, or switch the group off.`
    : `${line} must be complete before saving.`;
}

export interface Environment {
  name: string;
  orphaned: boolean;
}

export function environmentsOf(current: State): Environment[] {
  const out = current.environments.map((name) => ({ name, orphaned: false }));
  const seen = new Set(current.environments);
  for (const row of current.matrix.rows) {
    for (const cell of row.cells) {
      for (const override of cell.overrides ?? []) {
        if (override.orphaned && !seen.has(override.environment)) {
          seen.add(override.environment);
          out.push({ name: override.environment, orphaned: true });
        }
      }
    }
  }
  return out;
}

export interface Lens {
  environment: string;
  query: string;
  owedOnly: boolean;
}

export type Inherits = "root" | "base" | null;

export interface KeyLine {
  row: MatrixRow;
  variant: Variant;
  inherits: Inherits;
  overrides: string[];
  orphaned: boolean;
  needed: boolean;
}

export interface Group {
  folder: string;
  keys: number;
  owed: number;
  lines: KeyLine[];
}

export interface Listing {
  flat: boolean;
  keys: KeyLine[];
  groups: Group[];
}

function lineOf(
  catalogue: Catalogue,
  owed: ReadonlySet<string>,
  row: MatrixRow,
  cell: MatrixCell,
  environment: string,
): KeyLine {
  const at = { key: row.key, folder: cell.folder, environment };
  const variant =
    catalogue.variants.get(addressKey(at)) ??
    variantOf(row, cell, environment, true, catalogue.off);
  const root = cellOf(row, "");
  let inherits: Inherits = null;
  if (environment !== "") {
    inherits = overrideOf(cell, environment) ? null : "base";
  } else if (
    cell.folder !== "" &&
    !cell.set &&
    !cell.reference &&
    root !== undefined &&
    root.state !== "forbidden"
  ) {
    inherits = "root";
  }
  return {
    row,
    variant,
    inherits,
    overrides: (cell.overrides ?? []).map((override) => override.environment),
    orphaned: (cell.overrides ?? []).some((override) => override.orphaned === true),
    needed: owed.has(addressKey(at)),
  };
}

function listed(cell: MatrixCell): boolean {
  return cell.state !== "forbidden" || cell.set;
}

function matches(key: string, query: string): boolean {
  return key.toLowerCase().includes(query.trim().toLowerCase());
}

export function listingOf(
  current: State,
  catalogue: Catalogue,
  owed: ReadonlySet<string>,
  lens: Lens,
): Listing {
  const flat = lens.owedOnly || lens.query.trim() !== "";
  const keys: KeyLine[] = [];
  for (const row of current.matrix.rows) {
    if (lens.owedOnly) {
      for (const cell of row.cells) {
        if (owed.has(addressKey({ key: row.key, folder: cell.folder, environment: "" }))) {
          keys.push(lineOf(catalogue, owed, row, cell, ""));
        }
      }
      continue;
    }
    if (flat) {
      if (!matches(row.key, lens.query)) continue;
      for (const cell of row.cells) {
        if (listed(cell)) keys.push(lineOf(catalogue, owed, row, cell, lens.environment));
      }
      continue;
    }
    const root = cellOf(row, "") ?? forbiddenRoot;
    keys.push(lineOf(catalogue, owed, row, root, listed(root) ? lens.environment : ""));
  }
  const groups: Group[] = [];
  if (!flat) {
    for (const folder of current.matrix.columns) {
      if (folder === "") continue;
      const lines: KeyLine[] = [];
      let owing = 0;
      for (const row of current.matrix.rows) {
        const cell = cellOf(row, folder);
        if (!cell || !listed(cell)) continue;
        if (!catalogue.variants.has(addressKey({ key: row.key, folder, environment: "" })))
          continue;
        lines.push(lineOf(catalogue, owed, row, cell, lens.environment));
        if (owedCell(row, cell, catalogue.off)) owing += 1;
      }
      groups.push({ folder, keys: lines.length, owed: owing, lines });
    }
  }
  return { flat, keys, groups };
}

export function setForOptions(catalogue: Catalogue, row: MatrixRow): string[] {
  return readable(row).filter(
    (folder) =>
      folder !== "" &&
      !catalogue.variants.has(addressKey({ key: row.key, folder, environment: "" })),
  );
}

export function overrideOptions(current: State, catalogue: Catalogue, at: Cell): string[] {
  return current.environments.filter(
    (environment) => !catalogue.variants.has(addressKey({ ...at, environment })),
  );
}

export function whenLine(createdAt: number): string {
  return new Date(createdAt * 1000).toLocaleString(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  });
}

export function sizeLine(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  return `${(bytes / 1024).toFixed(1)} KiB`;
}

export function revealable(variant: Variant): boolean {
  return variant.set && variant.class !== "secret";
}

export function editable(variant: Variant): boolean {
  return variant.state !== "forbidden" || variant.set;
}

export function folderName(folder: string): string {
  return folder === "" ? "root" : folder;
}

const namedLimit = 5;

export function names(items: string[]): string {
  const shown = items.slice(0, namedLimit);
  const rest = items.length - shown.length;
  if (rest > 0) shown.push(`${rest} other${rest === 1 ? "" : "s"}`);
  if (shown.length <= 2) return shown.join(" and ");
  return `${shown.slice(0, -1).join(", ")} and ${shown[shown.length - 1]}`;
}

export function plural(count: number, noun: string): string {
  return `${count} ${noun}${count === 1 ? "" : "s"}`;
}

export interface Draft {
  at: Address;
  value: string;
  version: number;
}

export function baselineOf(at: Address, baselines: ReadonlyMap<string, string>): string {
  return baselines.get(addressKey(at)) ?? "";
}

export function isDirty(
  at: Address,
  drafts: ReadonlyMap<string, string>,
  baselines: ReadonlyMap<string, string>,
): boolean {
  const draft = drafts.get(addressKey(at));
  return draft !== undefined && draft !== baselineOf(at, baselines);
}

export function dirtyEntries(
  catalogue: Catalogue,
  drafts: ReadonlyMap<string, string>,
  baselines: ReadonlyMap<string, string>,
): Draft[] {
  const out: Draft[] = [];
  for (const variant of catalogue.variants.values()) {
    if (variant.reference) continue;
    if (!isDirty(variant.at, drafts, baselines)) continue;
    out.push({
      at: variant.at,
      value: drafts.get(addressKey(variant.at))!,
      version: variant.version,
    });
  }
  return out;
}

export type SaveResult =
  | { at: Address; ok: true }
  | { at: Address; ok: false; status: number; message: string };

export interface Problem {
  kind: "conflict" | "error";
  message: string;
}

export interface SaveOutcome {
  drafts: Map<string, string>;
  baselines: Map<string, string>;
  problems: Map<string, Problem>;
  saved: number;
  conflicted: number;
  failed: number;
}

export function reduceSave(
  drafts: ReadonlyMap<string, string>,
  baselines: ReadonlyMap<string, string>,
  problems: ReadonlyMap<string, Problem>,
  results: readonly SaveResult[],
): SaveOutcome {
  const out: SaveOutcome = {
    drafts: new Map(drafts),
    baselines: new Map(baselines),
    problems: new Map(problems),
    saved: 0,
    conflicted: 0,
    failed: 0,
  };
  for (const result of results) {
    const key = addressKey(result.at);
    if (result.ok) {
      const value = out.drafts.get(key);
      if (value !== undefined && out.baselines.has(key)) {
        out.baselines.set(key, value);
      }
      out.drafts.delete(key);
      out.problems.delete(key);
      out.saved += 1;
    } else if (result.status === 409) {
      out.problems.set(key, { kind: "conflict", message: result.message });
      out.conflicted += 1;
    } else {
      out.problems.set(key, { kind: "error", message: result.message });
      out.failed += 1;
    }
  }
  return out;
}

export function saveSummary(outcome: SaveOutcome): string {
  const total = outcome.saved + outcome.conflicted + outcome.failed;
  if (total === 0) return "Nothing to save.";
  if (outcome.saved === total) return `Saved ${plural(total, "change")}.`;
  const why: string[] = [];
  if (outcome.conflicted > 0) {
    why.push(`${outcome.conflicted} changed underneath you`);
  }
  if (outcome.failed > 0) why.push(`${outcome.failed} failed`);
  return `Saved ${outcome.saved} of ${plural(total, "change")}; ${names(why)} — those rows stay unsaved.`;
}

export function removeSummary(outcome: SaveOutcome): string {
  const total = outcome.saved + outcome.conflicted + outcome.failed;
  if (outcome.saved === total) return `Removed ${plural(total, "value")}.`;
  const why: string[] = [];
  if (outcome.conflicted > 0) {
    why.push(`${outcome.conflicted} changed underneath you`);
  }
  if (outcome.failed > 0) why.push(`${outcome.failed} failed`);
  return `Removed ${outcome.saved} of ${plural(total, "value")}; ${names(why)} — see the marked rows.`;
}

export interface Fill {
  at: Address;
  value: string;
  materialise: boolean;
}

export interface Skipped {
  key: string;
  reason: string;
}

export interface DropOutcome {
  folder: string;
  fills: Fill[];
  undeclared: string[];
  skipped: Skipped[];
}

export function applyDotenv(
  catalogue: Catalogue,
  entries: readonly { key: string; value: string }[],
  folder: string,
): DropOutcome {
  const out: DropOutcome = { folder, fills: [], undeclared: [], skipped: [] };
  const where = folderName(folder);
  for (const entry of entries) {
    const row = catalogue.rows.find((candidate) => candidate.key === entry.key);
    if (!row) {
      out.undeclared.push(entry.key);
      continue;
    }
    if (!readable(row).includes(folder)) {
      out.skipped.push({
        key: entry.key,
        reason: `nothing reads ${entry.key} in ${where}`,
      });
      continue;
    }
    const at = { key: entry.key, folder, environment: "" };
    const variant = catalogue.variants.get(addressKey(at));
    if (variant?.reference) {
      out.skipped.push({
        key: entry.key,
        reason: `${entry.key} in ${where} reads ${variant.reference.slug}; change it with ocel env ref in the terminal`,
      });
      continue;
    }
    out.fills.push({
      at,
      value: entry.value,
      materialise: variant === undefined || variant.extra,
    });
  }
  return out;
}

export interface OtherValue extends Address {
  version: number;
  class: Class;
  reference?: Reference;
  value?: string;
  error?: string;
}

export interface CopyCell {
  at: Address;
  class: Class;
  there: string | undefined;
  hereSet: boolean;
  hereVersion: number;
  materialise: boolean;
}

export interface CopyPlan {
  fills: CopyCell[];
  overwrites: CopyCell[];
  unreadable: { at: Address; error: string }[];
  skipped: Skipped[];
}

export function planCopy(
  catalogue: Catalogue,
  environments: readonly string[],
  other: readonly OtherValue[],
): CopyPlan {
  const plan: CopyPlan = { fills: [], overwrites: [], unreadable: [], skipped: [] };
  for (const value of other) {
    const at = { key: value.key, folder: value.folder, environment: value.environment };
    const row = catalogue.rows.find((candidate) => candidate.key === value.key);
    const where = folderName(value.folder);
    if (!row) {
      plan.skipped.push({ key: value.key, reason: `${value.key} is not declared here` });
      continue;
    }
    if (!readable(row).includes(value.folder)) {
      plan.skipped.push({ key: value.key, reason: `nothing reads ${value.key} in ${where} here` });
      continue;
    }
    if (value.environment !== "" && !environments.includes(value.environment)) {
      plan.skipped.push({
        key: value.key,
        reason: `no environment named ${value.environment} exists here`,
      });
      continue;
    }
    const variant = catalogue.variants.get(addressKey(at));
    if (variant?.reference) {
      plan.skipped.push({
        key: value.key,
        reason: `${value.key} in ${where} reads ${variant.reference.slug} here; a copy would break the link`,
      });
      continue;
    }
    if (value.error !== undefined) {
      plan.unreadable.push({ at, error: value.error });
      continue;
    }
    const cell: CopyCell = {
      at,
      class: value.class,
      there: value.value,
      hereSet: variant?.set === true,
      hereVersion: variant?.version ?? 0,
      materialise: variant === undefined || variant.extra,
    };
    (cell.hereSet ? plan.overwrites : plan.fills).push(cell);
  }
  return plan;
}

export interface CopyBranch {
  folder: string;
  cells: CopyCell[];
}

export function copyTree(plan: CopyPlan): CopyBranch[] {
  const branches = new Map<string, CopyCell[]>();
  for (const cell of [...plan.fills, ...plan.overwrites]) {
    const held = branches.get(cell.at.folder) ?? [];
    held.push(cell);
    branches.set(cell.at.folder, held);
  }
  return [...branches.entries()]
    .sort(([a], [b]) => (a === "" ? -1 : b === "" ? 1 : a.localeCompare(b)))
    .map(([folder, cells]) => ({ folder, cells }));
}

export function referenceLine(reference: Reference): string {
  return reference.folder === ""
    ? `${reference.slug}/${reference.key}`
    : `${reference.slug}${reference.folder}/${reference.key}`;
}

export function owedSet(recovery: Recovery | undefined): ReadonlySet<string> {
  return new Set(
    (recovery?.owed ?? []).map((cell) =>
      addressKey({ key: cell.key, folder: cell.folder, environment: "" }),
    ),
  );
}

export function unfilledOwed(
  catalogue: Catalogue,
  owed: ReadonlySet<string>,
  drafts: ReadonlyMap<string, string>,
  baselines: ReadonlyMap<string, string>,
): Variant[] {
  return variantsOf(catalogue).filter((variant) => {
    const key = addressKey(variant.at);
    if (!owed.has(key)) return false;
    if (isDirty(variant.at, drafts, baselines)) return false;
    return !variant.set || variant.problem !== undefined;
  });
}

export function doneLabel(owed: number): string {
  return owed === 0
    ? "Return to the terminal"
    : `Return with ${plural(owed, "cell")} still to fill`;
}

export function tallyLine(owed: number): string {
  return owed === 0 ? "every required cell is filled" : `${plural(owed, "cell")} to fill`;
}
