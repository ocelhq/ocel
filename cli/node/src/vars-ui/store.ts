import { signal } from "@preact/signals";

import { api, ApiError, query } from "./api";
import {
  sameAddress,
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

export async function load(): Promise<void> {
  try {
    state.value = await api<State>("GET", "/api/state");
  } catch (thrown) {
    farewell.value = `Could not read this project's variables: ${thrown instanceof Error ? thrown.message : String(thrown)}`;
  }
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

export function leave(): void {
  void api("POST", "/api/done").catch(() => {
  });
  farewell.value = "Returned to the terminal. You can close this tab.";
}
