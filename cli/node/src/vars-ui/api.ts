import type { Address } from "./model";

const token = new URLSearchParams(location.hash.slice(1)).get("t") ?? "";
history.replaceState(null, "", location.pathname);

export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

export async function api<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
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
    } catch {
    }
    throw new ApiError(response.status, message.trim());
  }
  return text ? (JSON.parse(text) as T) : (undefined as T);
}

export function query(at: Address): string {
  return `key=${encodeURIComponent(at.key)}&folder=${encodeURIComponent(at.folder)}&environment=${encodeURIComponent(at.environment)}`;
}
