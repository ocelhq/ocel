import type {
  Address,
  CopyResult,
  Revealed,
  SessionPort,
  State,
  VarsPort,
  Version,
} from "@ui/vars";
import { VarsError } from "@ui/vars";

const token = new URLSearchParams(location.hash.slice(1)).get("t") ?? "";
history.replaceState(null, "", location.pathname);

async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch(path, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await response.text();
  if (!response.ok) {
    let message = text;
    try {
      message = JSON.parse(text).error ?? text;
    } catch {}
    throw new VarsError(response.status, message.trim());
  }
  return text ? (JSON.parse(text) as T) : (undefined as T);
}

function query(at: Address): string {
  return `key=${encodeURIComponent(at.key)}&folder=${encodeURIComponent(at.folder)}&environment=${encodeURIComponent(at.environment)}`;
}

export const loopback: VarsPort = {
  read: () => api<State>("GET", "/api/state"),
  reveal: (cells) => api<Revealed>("POST", "/api/reveal", { cells }),
  set: (at, value, version) => api("PUT", "/api/value", { ...at, value, version }),
  remove: (at, version) => api("DELETE", `/api/value?${query(at)}&version=${version}`),
  history: async (at) =>
    (await api<{ versions: Version[] }>("GET", `/api/history?${query(at)}`)).versions,
  other: () => api("GET", "/api/other"),
  copy: async (cells) =>
    (await api<{ results: CopyResult[] }>("POST", "/api/copy", { cells })).results,
};

export const session: SessionPort = {
  attend: async () => {
    const response = await fetch("/api/presence", {
      headers: { Authorization: `Bearer ${token}` },
      cache: "no-store",
    });
    if (!response.ok || response.body === null) {
      throw new VarsError(response.status, await response.text());
    }
    const reader = response.body.getReader();
    while (!(await reader.read()).done) {}
  },
  done: () => api("POST", "/api/done"),
  abandon: () => api("POST", "/api/abandon"),
};
