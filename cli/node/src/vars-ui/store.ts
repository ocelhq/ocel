import { computed, signal } from "./signals";

import { api, ApiError, hold, query } from "./api";
import { parseDotenv } from "./dotenv";
import {
  addressKey,
  applyDotenv,
  baselineOf,
  catalogueOf,
  dirtyEntries,
  editable,
  environmentsOf,
  listingOf,
  names,
  owedSet,
  planCopy,
  plural,
  reduceSave,
  removeSummary,
  revealable,
  saveSummary,
  unfilledOwed,
  variantsOf,
  type Address,
  type AppResolution,
  type CopyPlan,
  type DropOutcome,
  type OtherValue,
  type Problem,
  type SaveResult,
  type State,
  type Version,
} from "./model";

export const state = signal<State | null>(null);
export const hoveredApp = signal<AppResolution | null>(null);
export const saving = signal(false);
export const farewell = signal<string | null>(null);

export const environment = signal("");
export const search = signal("");
export const owedOnly = signal(false);
export const selected = signal<ReadonlySet<string>>(new Set());
export const extras = signal<readonly Address[]>([]);
export const expanded = signal<ReadonlySet<string>>(new Set());
export const spotlight = signal<string | null>(null);
export const focusing = signal<string | null>(null);

export const drafts = signal<ReadonlyMap<string, string>>(new Map());
export const baselines = signal<ReadonlyMap<string, string>>(new Map());
export const revealErrors = signal<ReadonlyMap<string, string>>(new Map());
export const problems = signal<ReadonlyMap<string, Problem>>(new Map());
export const outcome = signal<{ text: string; tone?: "owed" } | null>(null);

export const dragTarget = signal<string | null>(null);
export const dropped = signal<(DropOutcome & { name: string }) | null>(null);

export interface Removal {
  cells: { at: Address; version: number }[];
}

export const removing = signal<Removal | null>(null);

export interface CopyDialog {
  tier: string;
  plan: CopyPlan;
  chosen: ReadonlySet<string>;
  overwriting: boolean;
  open: ReadonlySet<string>;
  busy: boolean;
  error: string | null;
}

export const copying = signal<CopyDialog | null>(null);
export const copyLoading = signal(false);

export const drawer = signal<Address | null>(null);
export const history = signal<Version[] | null>(null);
export const historyError = signal<string | null>(null);

const emptyState: State = {
  slug: "",
  tier: "",
  other: "",
  environments: [],
  matrix: { columns: [], rows: [], apps: [] },
};

export const catalogue = computed(() =>
  catalogueOf(state.value ?? emptyState, extras.value),
);

export const variants = computed(() => catalogue.value.variants);

export const environments = computed(() =>
  state.value ? environmentsOf(state.value) : [],
);

export const dirty = computed(() =>
  dirtyEntries(catalogue.value, drafts.value, baselines.value),
);

export const owed = computed(() => owedSet(state.value?.recovery));

export const listing = computed(() =>
  listingOf(state.value ?? emptyState, catalogue.value, owed.value, {
    environment: environment.value,
    query: search.value,
    owedOnly: owedOnly.value,
  }),
);

export const visible = computed(() => {
  const shown = listing.value;
  const lines = [
    ...shown.keys,
    ...shown.groups
      .filter((group) => expanded.value.has(group.folder))
      .flatMap((group) => group.lines),
  ];
  return lines.filter((line) => editable(line.variant)).map((line) => line.variant);
});

export const unfilled = computed(() =>
  unfilledOwed(catalogue.value, owed.value, drafts.value, baselines.value),
);

export const finishing = signal(false);
export const finishError = signal<string | null>(null);

export async function load(): Promise<void> {
  try {
    state.value = await api<State>("GET", "/api/state");
  } catch (thrown) {
    farewell.value = `Could not read this project's variables: ${message(thrown)}`;
    return;
  }
  void attend();
  owedOnly.value = unfilled.value.length > 0;
  expanded.value = new Set(
    listing.value.groups.filter((group) => group.owed > 0).map((group) => group.folder),
  );
}

async function attend(): Promise<void> {
  while (farewell.value === null) {
    try {
      await hold("/api/presence");
    } catch {
    }
    if (farewell.value !== null) return;
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
}

function message(thrown: unknown): string {
  return thrown instanceof Error ? thrown.message : String(thrown);
}

async function refresh(): Promise<void> {
  try {
    state.value = await api<State>("GET", "/api/state");
  } catch (thrown) {
    outcome.value = {
      text: `The page could not re-read the variables, so what is on screen may be out of date: ${message(thrown)}`,
      tone: "owed",
    };
  }
}

export function toggleGroup(folder: string): void {
  const next = new Set(expanded.value);
  if (!next.delete(folder)) next.add(folder);
  expanded.value = next;
}

function expand(folder: string): void {
  if (folder === "" || expanded.value.has(folder)) return;
  expanded.value = new Set([...expanded.value, folder]);
}

export function revealGroup(folder: string): void {
  search.value = "";
  owedOnly.value = false;
  expand(folder);
  spotlight.value = folder;
}

export function spotlighted(): void {
  spotlight.value = null;
}

export function focused(): void {
  focusing.value = null;
}

export function pickEnvironment(name: string): void {
  environment.value = name;
  selected.value = new Set();
}

export function setSearch(text: string): void {
  search.value = text;
  selected.value = new Set();
}

export function showOwed(on: boolean): void {
  owedOnly.value = on;
  selected.value = new Set();
}

export function toggleSelected(at: Address): void {
  const next = new Set(selected.value);
  const key = addressKey(at);
  if (!next.delete(key)) next.add(key);
  selected.value = next;
}

export function selectVisible(on: boolean): void {
  selected.value = on ? new Set(visible.value.map((v) => addressKey(v.at))) : new Set();
}

export function clearSelection(): void {
  selected.value = new Set();
}

function remember(at: Address): void {
  if (variants.value.has(addressKey(at))) return;
  extras.value = [...extras.value, at];
}

export function addOverride(at: Address): void {
  remember(at);
  environment.value = at.environment;
  search.value = "";
  owedOnly.value = false;
  selected.value = new Set();
  expand(at.folder);
  focusing.value = addressKey(at);
}

export function setFor(at: Address): void {
  remember(at);
  expand(at.folder);
  focusing.value = addressKey(at);
}

export function dismiss(at: Address): void {
  const key = addressKey(at);
  extras.value = extras.value.filter((extra) => addressKey(extra) !== key);
  const next = new Map(drafts.value);
  next.delete(key);
  drafts.value = next;
}

export function setDraft(at: Address, value: string): void {
  remember(at);
  const key = addressKey(at);
  const next = new Map(drafts.value);
  if (value === baselineOf(at, baselines.value)) next.delete(key);
  else next.set(key, value);
  drafts.value = next;
}

export function discard(): void {
  drafts.value = new Map();
  problems.value = new Map();
  outcome.value = null;
}

interface Revealed {
  values: (Address & { value: string })[];
  errors: (Address & { error: string })[];
}

export async function reveal(cells: readonly Address[]): Promise<void> {
  const asked = cells.filter((at) => {
    const variant = variants.value.get(addressKey(at));
    return variant !== undefined && revealable(variant);
  });
  if (asked.length === 0) return;
  let read: Revealed;
  try {
    read = await api<Revealed>("POST", "/api/reveal", { cells: asked });
  } catch (thrown) {
    const errors = new Map(revealErrors.value);
    for (const at of asked) errors.set(addressKey(at), message(thrown));
    revealErrors.value = errors;
    return;
  }
  const values = new Map(baselines.value);
  const errors = new Map(revealErrors.value);
  for (const found of read.values) {
    values.set(addressKey(found), found.value);
    errors.delete(addressKey(found));
  }
  for (const failed of read.errors) {
    errors.set(addressKey(failed), failed.error);
    values.delete(addressKey(failed));
  }
  baselines.value = values;
  revealErrors.value = errors;
}

export function hide(cells: readonly Address[]): void {
  const values = new Map(baselines.value);
  const errors = new Map(revealErrors.value);
  for (const at of cells) {
    values.delete(addressKey(at));
    errors.delete(addressKey(at));
  }
  baselines.value = values;
  revealErrors.value = errors;
}

export const shown = computed(() => {
  const open = visible.value.filter(revealable);
  return open.length > 0 && open.every((v) => baselines.value.has(addressKey(v.at)));
});

export function toggleRevealVisible(): void {
  const cells = visible.value.filter(revealable).map((v) => v.at);
  if (shown.value) hide(cells);
  else void reveal(cells);
}

function chosen(): Address[] {
  return variantsOf(catalogue.value)
    .filter((v) => selected.value.has(addressKey(v.at)))
    .map((v) => v.at);
}

export function revealSelected(): Promise<void> {
  return reveal(chosen());
}

export function hideSelected(): void {
  hide(chosen());
}

export async function copyValue(at: Address): Promise<void> {
  if (!baselines.value.has(addressKey(at))) await reveal([at]);
  const value = baselines.value.get(addressKey(at));
  if (value === undefined) {
    outcome.value = { text: `Could not read the value of ${at.key} to copy it.`, tone: "owed" };
    return;
  }
  try {
    await navigator.clipboard.writeText(value);
    outcome.value = { text: `Copied the value of ${at.key}.` };
  } catch (thrown) {
    outcome.value = { text: `Could not copy: ${message(thrown)}`, tone: "owed" };
  }
}

async function attempt(at: Address, run: () => Promise<unknown>): Promise<SaveResult> {
  try {
    await run();
    return { at, ok: true };
  } catch (thrown) {
    return {
      at,
      ok: false,
      status: thrown instanceof ApiError ? thrown.status : 0,
      message: message(thrown),
    };
  }
}

function settle(results: SaveResult[]): ReturnType<typeof reduceSave> {
  const reduced = reduceSave(drafts.value, baselines.value, problems.value, results);
  drafts.value = reduced.drafts;
  baselines.value = reduced.baselines;
  problems.value = reduced.problems;
  return reduced;
}

export async function save(): Promise<void> {
  const pending = dirty.value;
  if (pending.length === 0 || saving.value) return;
  saving.value = true;
  outcome.value = null;
  try {
    const results = await Promise.all(
      pending.map((draft) =>
        attempt(draft.at, () =>
          api("PUT", "/api/value", {
            ...draft.at,
            value: draft.value,
            version: draft.version,
          }),
        ),
      ),
    );
    await refresh();
    const reduced = settle(results);
    outcome.value = {
      text: saveSummary(reduced),
      ...(reduced.saved < results.length && { tone: "owed" as const }),
    };
    await reveal(
      results
        .filter((result) => !result.ok && result.status === 409)
        .map((result) => result.at),
    );
  } finally {
    saving.value = false;
  }
}

export function askRemoval(cells: readonly Address[]): void {
  const held = cells
    .map((at) => variants.value.get(addressKey(at)))
    .filter((v) => v !== undefined && v.set && !v.reference)
    .map((v) => ({ at: v!.at, version: v!.version }));
  if (held.length === 0) return;
  removing.value = { cells: held };
}

export function cancelRemoval(): void {
  if (saving.value) return;
  removing.value = null;
}

export async function confirmRemoval(): Promise<void> {
  const asked = removing.value;
  if (!asked || saving.value) return;
  saving.value = true;
  outcome.value = null;
  try {
    const results = await Promise.all(
      asked.cells.map(({ at, version }) =>
        attempt(at, () => api("DELETE", `/api/value?${query(at)}&version=${version}`)),
      ),
    );
    await refresh();
    const reduced = settle(results);
    hide(results.filter((r) => r.ok).map((r) => r.at));
    outcome.value = {
      text: removeSummary(reduced),
      ...(reduced.saved < results.length && { tone: "owed" as const }),
    };
    removing.value = null;
    selected.value = new Set();
  } finally {
    saving.value = false;
  }
}

export function applyDrop(name: string, text: string, into: string): void {
  const out = applyDotenv(catalogue.value, parseDotenv(text), into);
  const added = [...extras.value];
  const known = new Set(added.map(addressKey));
  const next = new Map(drafts.value);
  for (const fill of out.fills) {
    if (fill.materialise && !known.has(addressKey(fill.at))) {
      added.push(fill.at);
      known.add(addressKey(fill.at));
    }
    if (fill.value === baselineOf(fill.at, baselines.value)) {
      next.delete(addressKey(fill.at));
    } else {
      next.set(addressKey(fill.at), fill.value);
    }
  }
  extras.value = added;
  drafts.value = next;
  dropped.value = { ...out, name };
  dragTarget.value = null;
  if (out.fills.length > 0) expand(into);
}

export function importFile(file: File, into: string): void {
  void file.text().then((text) => applyDrop(file.name, text, into));
}

export function dismissDrop(): void {
  dropped.value = null;
}

export async function openCopy(): Promise<void> {
  const current = state.value;
  if (!current || copyLoading.value) return;
  copyLoading.value = true;
  outcome.value = null;
  try {
    const other = await api<{ tier: string; values: OtherValue[] }>("GET", "/api/other");
    const plan = planCopy(catalogue.value, current.environments, other.values);
    copying.value = {
      tier: other.tier,
      plan,
      chosen: new Set(plan.fills.map((cell) => addressKey(cell.at))),
      overwriting: false,
      open: new Set([""]),
      busy: false,
      error: null,
    };
  } catch (thrown) {
    outcome.value = {
      text: `Could not read the ${current.other} values: ${message(thrown)}`,
      tone: "owed",
    };
  } finally {
    copyLoading.value = false;
  }
}

export function toggleCopy(cells: readonly Address[], on?: boolean): void {
  const dialog = copying.value;
  if (!dialog || dialog.busy) return;
  const next = new Set(dialog.chosen);
  for (const at of cells) {
    const key = addressKey(at);
    const want = on ?? !next.has(key);
    if (want) next.add(key);
    else next.delete(key);
  }
  copying.value = { ...dialog, chosen: next };
}

export function toggleOverwriting(): void {
  const dialog = copying.value;
  if (!dialog || dialog.busy) return;
  const overwriting = !dialog.overwriting;
  const chosen = new Set(dialog.chosen);
  if (!overwriting) {
    for (const cell of dialog.plan.overwrites) chosen.delete(addressKey(cell.at));
  }
  copying.value = { ...dialog, overwriting, chosen };
}

export function toggleBranch(target: string): void {
  const dialog = copying.value;
  if (!dialog) return;
  const open = new Set(dialog.open);
  if (!open.delete(target)) open.add(target);
  copying.value = { ...dialog, open };
}

export function closeCopy(): void {
  if (copying.value?.busy) return;
  copying.value = null;
}

interface CopyResult extends Address {
  saved: boolean;
  conflict?: boolean;
  error?: string;
}

export async function confirmCopy(): Promise<void> {
  const dialog = copying.value;
  if (!dialog || dialog.busy) return;
  const cells = [...dialog.plan.fills, ...dialog.plan.overwrites].filter((cell) =>
    dialog.chosen.has(addressKey(cell.at)),
  );
  if (cells.length === 0) return;
  copying.value = { ...dialog, busy: true, error: null };
  saving.value = true;
  try {
    const answer = await api<{ results: CopyResult[] }>("POST", "/api/copy", {
      cells: cells.map((cell) => ({ ...cell.at, version: cell.hereVersion })),
    });
    const added = [...extras.value];
    const known = new Set(added.map(addressKey));
    for (const cell of cells) {
      if (cell.materialise && !known.has(addressKey(cell.at))) {
        added.push(cell.at);
        known.add(addressKey(cell.at));
      }
    }
    extras.value = added;
    await refresh();
    const results: SaveResult[] = answer.results.map((result) => {
      const at = { key: result.key, folder: result.folder, environment: result.environment };
      if (result.saved) return { at, ok: true };
      return {
        at,
        ok: false,
        status: result.conflict ? 409 : 0,
        message: result.error ?? "the copy failed",
      };
    });
    const reduced = settle(results);
    const total = results.length;
    const why: string[] = [];
    if (reduced.conflicted > 0) why.push(`${reduced.conflicted} changed here underneath you`);
    if (reduced.failed > 0) why.push(`${reduced.failed} failed`);
    outcome.value =
      reduced.saved === total
        ? { text: `Copied ${plural(total, "value")} from ${dialog.tier}.` }
        : {
            text: `Copied ${reduced.saved} of ${plural(total, "value")} from ${dialog.tier}; ${names(why)} — see the marked rows.`,
            tone: "owed",
          };
    copying.value = null;
    await reveal(results.filter((r) => !r.ok && r.status === 409).map((r) => r.at));
  } catch (thrown) {
    copying.value = { ...dialog, busy: false, error: message(thrown) };
  } finally {
    saving.value = false;
  }
}

export function openDrawer(at: Address): void {
  drawer.value = at;
  history.value = null;
  historyError.value = null;
  void api<{ versions: Version[] }>("GET", `/api/history?${query(at)}`).then(
    (read) => {
      if (drawer.value && addressKey(drawer.value) === addressKey(at)) {
        history.value = read.versions;
      }
    },
    (thrown: unknown) => {
      if (drawer.value && addressKey(drawer.value) === addressKey(at)) {
        historyError.value = message(thrown);
      }
    },
  );
}

export function closeDrawer(): void {
  drawer.value = null;
}

export function leave(): void {
  void api("POST", "/api/done").catch(() => {
  });
  farewell.value = "Returned to the terminal. You can close this tab.";
}

export function leaveDiscarding(): void {
  discard();
  leave();
}

export async function resume(): Promise<void> {
  if (finishing.value || saving.value) return;
  finishing.value = true;
  finishError.value = null;
  try {
    if (dirty.value.length > 0) {
      await save();
      if (dirty.value.length > 0) {
        finishError.value =
          "Some rows did not save, so the deploy cannot resume yet; sort them out above and try again.";
        return;
      }
    }
    if (unfilled.value.length > 0) {
      finishError.value = `The deploy still needs ${plural(unfilled.value.length, "cell")} filled.`;
      return;
    }
    await api("POST", "/api/done");
    farewell.value = "Saved. The deploy is resuming in the terminal; you can close this tab.";
  } catch (thrown) {
    finishError.value = message(thrown);
  } finally {
    finishing.value = false;
  }
}

export async function abandon(): Promise<void> {
  if (finishing.value) return;
  finishing.value = true;
  finishError.value = null;
  try {
    await api("POST", "/api/abandon");
    farewell.value =
      "Abandoned. The deploy fails in the terminal with the cells it was refused; you can close this tab.";
  } catch (thrown) {
    finishError.value = message(thrown);
  } finally {
    finishing.value = false;
  }
}
