import { computed, signal } from "@preact/signals";

import { api, ApiError, query } from "./api";
import {
  addressKey,
  baselineOf,
  dirtyEntries,
  reduceSave,
  saveSummary,
  treeOf,
  type Address,
  type AppResolution,
  type Problem,
  type SaveResult,
  type State,
} from "./model";

export const state = signal<State | null>(null);
export const hoveredApp = signal<AppResolution | null>(null);
export const saving = signal(false);
export const farewell = signal<string | null>(null);

export const expanded = signal<ReadonlySet<string>>(new Set());
export const extras = signal<readonly Address[]>([]);

export const drafts = signal<ReadonlyMap<string, string>>(new Map());
export const baselines = signal<ReadonlyMap<string, string>>(new Map());
export const problems = signal<ReadonlyMap<string, Problem>>(new Map());
export const outcome = signal<{ text: string; tone?: "owed" } | null>(null);

export const tree = computed(() =>
  state.value ? treeOf(state.value, extras.value) : [],
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
    const reduced = reduceSave(drafts.value, problems.value, results);
    drafts.value = reduced.drafts;
    problems.value = reduced.problems;
    outcome.value = {
      text: saveSummary(reduced),
      ...(reduced.saved < results.length && { tone: "owed" as const }),
    };
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
    const reduced = reduceSave(drafts.value, problems.value, [result]);
    drafts.value = reduced.drafts;
    problems.value = reduced.problems;
    outcome.value = result.ok
      ? { text: `Removed the value of ${at.key}.` }
      : { text: `Could not remove the value of ${at.key}: ${result.message}`, tone: "owed" };
  } finally {
    saving.value = false;
  }
}

export function leave(): void {
  void api("POST", "/api/done").catch(() => {
  });
  farewell.value = "Returned to the terminal. You can close this tab.";
}
