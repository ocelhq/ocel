import { computed, signal } from "@preact/signals";

import { api, ApiError, query } from "./api";
import {
  addressKey,
  sameAddress,
  treeOf,
  type Address,
  type AppResolution,
  type State,
  type Version,
} from "./model";

export const state = signal<State | null>(null);
export const selected = signal<Address | null>(null);
export const versions = signal<Version[]>([]);
export const draft = signal("");
export const error = signal("");
export const hoveredApp = signal<AppResolution | null>(null);
export const saving = signal(false);
export const farewell = signal<string | null>(null);

export const expanded = signal<ReadonlySet<string>>(new Set());
export const extras = signal<readonly Address[]>([]);

export const tree = computed(() =>
  state.value ? treeOf(state.value, extras.value) : [],
);

export async function load(): Promise<void> {
  try {
    state.value = await api<State>("GET", "/api/state");
  } catch (thrown) {
    farewell.value = `Could not read this project's variables: ${thrown instanceof Error ? thrown.message : String(thrown)}`;
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
  address(at);
}

async function refreshHistory(): Promise<void> {
  versions.value = [];
  const at = selected.value;
  if (!at) return;
  const read = await api<{ versions: Version[] }>(
    "GET",
    `/api/history?${query(at)}`,
  );
  if (selected.value && sameAddress(selected.value, at)) {
    versions.value = read.versions;
  }
}

export function address(at: Address | null): void {
  selected.value = at;
  draft.value = "";
  error.value = "";
  versions.value = [];
  void refreshHistory().catch(() => {
  });
}

export async function mutate(run: () => Promise<void>): Promise<void> {
  saving.value = true;
  error.value = "";
  try {
    await run();
    state.value = await api<State>("GET", "/api/state");
    draft.value = "";
    await refreshHistory();
  } catch (thrown) {
    error.value = thrown instanceof Error ? thrown.message : String(thrown);
    if (thrown instanceof ApiError && thrown.status === 409) {
      error.value =
        "This value changed since the page read it; the page is showing it again — make your change against the value that is there now.";
      try {
        state.value = await api<State>("GET", "/api/state");
        await refreshHistory();
      } catch {
        error.value = `${error.value} The page could not re-read this cell either, so what is on screen may still be out of date.`;
      }
    }
  } finally {
    saving.value = false;
  }
}

export function erase(at: Address, version: number): Promise<void> {
  return mutate(() =>
    api("DELETE", `/api/value?${query(at)}&version=${version}`),
  );
}

export function leave(): void {
  void api("POST", "/api/done").catch(() => {
  });
  farewell.value = "Returned to the terminal. You can close this tab.";
}
