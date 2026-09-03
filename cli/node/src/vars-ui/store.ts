import { computed, signal } from "@preact/signals";

import { api, ApiError, query } from "./api";
import { parseDotenv } from "./dotenv";
import {
  addressKey,
  applyDotenv,
  baselineOf,
  dirtyEntries,
  reduceSave,
  revealable,
  saveSummary,
  treeOf,
  variantsOf,
  type Address,
  type AppResolution,
  type DropOutcome,
  type Problem,
  type SaveResult,
  type State,
  type Variant,
  type Version,
} from "./model";

export const state = signal<State | null>(null);
export const hoveredApp = signal<AppResolution | null>(null);
export const saving = signal(false);
export const farewell = signal<string | null>(null);

export const expanded = signal<ReadonlySet<string>>(new Set());
export const extras = signal<readonly Address[]>([]);

export const drafts = signal<ReadonlyMap<string, string>>(new Map());
export const baselines = signal<ReadonlyMap<string, string>>(new Map());
export const revealErrors = signal<ReadonlyMap<string, string>>(new Map());
export const problems = signal<ReadonlyMap<string, Problem>>(new Map());
export const outcome = signal<{ text: string; tone?: "owed" } | null>(null);

export const dropTarget = signal<string | null>(null);
export const dropped = signal<(DropOutcome & { name: string }) | null>(null);

export const drawer = signal<Address | null>(null);
export const history = signal<Version[] | null>(null);
export const historyError = signal<string | null>(null);

export const tree = computed(() =>
  state.value ? treeOf(state.value, extras.value) : [],
);

export const variants = computed(
  () => new Map(variantsOf(tree.value).map((v) => [addressKey(v.at), v])),
);

export const dirty = computed(() =>
  dirtyEntries(tree.value, drafts.value, baselines.value),
);

export async function load(): Promise<void> {
  try {
    state.value = await api<State>("GET", "/api/state");
  } catch (thrown) {
    farewell.value = `Could not read this project's variables: ${message(thrown)}`;
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

export function toggle(key: string): void {
  const next = new Set(expanded.value);
  if (!next.delete(key)) next.add(key);
  expanded.value = next;
}

export function expandAll(): void {
  expanded.value = new Set(tree.value.map((row) => row.key));
}

export function collapseAll(): void {
  expanded.value = new Set();
}

export function addVariant(at: Address): void {
  const known = new Set(extras.value.map(addressKey));
  if (!known.has(addressKey(at))) extras.value = [...extras.value, at];
  if (!expanded.value.has(at.key)) toggle(at.key);
}

export function dropVariant(at: Address): void {
  const key = addressKey(at);
  extras.value = extras.value.filter((extra) => addressKey(extra) !== key);
  setDraft(at, baselineOf(at, baselines.value));
}

export function setDraft(at: Address, value: string): void {
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

function everyRevealable(): Address[] {
  return variantsOf(tree.value)
    .filter(revealable)
    .map((variant: Variant) => variant.at);
}

export function revealAll(): Promise<void> {
  return reveal(everyRevealable());
}

export function hideAll(): void {
  hide(everyRevealable());
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

export async function erase(at: Address, version: number): Promise<void> {
  if (saving.value) return;
  saving.value = true;
  outcome.value = null;
  try {
    const result = await attempt(at, () =>
      api("DELETE", `/api/value?${query(at)}&version=${version}`),
    );
    await refresh();
    settle([result]);
    if (result.ok) hide([at]);
    outcome.value = result.ok
      ? { text: `Removed the value of ${at.key}.` }
      : { text: `Could not remove the value of ${at.key}: ${result.message}`, tone: "owed" };
  } finally {
    saving.value = false;
  }
}

export function applyDrop(name: string, text: string, folder: string): void {
  const out = applyDotenv(tree.value, parseDotenv(text), folder);
  const added = [...extras.value];
  const known = new Set(added.map(addressKey));
  const open = new Set(expanded.value);
  const next = new Map(drafts.value);
  for (const fill of out.fills) {
    if (fill.materialise && !known.has(addressKey(fill.at))) {
      added.push(fill.at);
      known.add(addressKey(fill.at));
    }
    if (fill.at.folder !== "") open.add(fill.at.key);
    if (fill.value === baselineOf(fill.at, baselines.value)) {
      next.delete(addressKey(fill.at));
    } else {
      next.set(addressKey(fill.at), fill.value);
    }
  }
  extras.value = added;
  expanded.value = open;
  drafts.value = next;
  dropped.value = { ...out, name };
  dropTarget.value = null;
}

export function dismissDrop(): void {
  dropped.value = null;
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
