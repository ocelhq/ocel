import { entryObjectKey } from "@framework/next-cache";
import type { Refresh } from "@framework/next-runtime/refresh";

export interface RefreshTask {
  isrPrefix: string;
  refresh: Refresh;
}

function staysOnInstance(url: unknown): url is string {
  return (
    typeof url === "string" &&
    url.startsWith("/") &&
    !url.startsWith("//") &&
    !/[^\x21-\x7e]/.test(url) &&
    !url.includes("\\")
  );
}

function readHeaders(headers: unknown): Record<string, string> | undefined {
  if (typeof headers !== "object" || headers === null || Array.isArray(headers)) return undefined;
  const read: Record<string, string> = {};
  for (const [name, value] of Object.entries(headers)) {
    if (typeof value !== "string") return undefined;
    read[name] = value;
  }
  return read;
}

export function readRefreshTask(body: string): RefreshTask | undefined {
  let parsed: unknown;
  try {
    parsed = JSON.parse(body);
  } catch {
    return undefined;
  }
  if (typeof parsed !== "object" || parsed === null) return undefined;
  const { isrPrefix, refresh } = parsed as { isrPrefix?: unknown; refresh?: unknown };
  if (typeof isrPrefix !== "string" || isrPrefix === "") return undefined;
  if (typeof refresh !== "object" || refresh === null) return undefined;
  const { url, key, lastModified, headers } = refresh as Record<string, unknown>;
  if (!staysOnInstance(url)) return undefined;
  if (typeof key !== "string" || entryObjectKey("", key) === null) return undefined;
  if (typeof lastModified !== "number" || !Number.isFinite(lastModified)) return undefined;
  const readable = readHeaders(headers);
  if (!readable) return undefined;
  return { isrPrefix, refresh: { url, key, lastModified, headers: readable } };
}
