import type http from "node:http";

export interface WriteHeadHeaders {
  at: number;
  passed: unknown;
}

export function readWriteHeadHeaders(args: unknown[]): WriteHeadHeaders {
  const at = typeof args[1] === "string" ? 2 : 1;
  return { at, passed: args.length > at ? args[at] : undefined };
}

function isPairs(headers: unknown[]): headers is unknown[][] {
  return headers.length > 0 && Array.isArray(headers[0]);
}

export function readWriteHeadHeader(headers: unknown, name: string): string | null {
  const values: string[] = [];
  if (Array.isArray(headers)) {
    if (isPairs(headers)) {
      for (const pair of headers) {
        if (String(pair[0]).toLowerCase() === name) values.push(String(pair[1]));
      }
    } else {
      for (let i = 0; i + 1 < headers.length; i += 2) {
        if (String(headers[i]).toLowerCase() === name) values.push(String(headers[i + 1]));
      }
    }
  } else if (headers !== null && typeof headers === "object") {
    for (const [key, value] of Object.entries(headers)) {
      if (key.toLowerCase() === name) values.push(String(value));
    }
  }
  return values.length > 0 ? values.join(", ") : null;
}

export function withoutWriteHeadHeaders(headers: unknown, names: readonly string[]): unknown {
  const dropped = (name: unknown) => names.includes(String(name).toLowerCase());
  if (Array.isArray(headers)) {
    if (isPairs(headers)) return headers.filter((pair) => !dropped(pair[0])).flat();
    const kept: unknown[] = [];
    for (let i = 0; i + 1 < headers.length; i += 2) {
      if (!dropped(headers[i])) kept.push(headers[i], headers[i + 1]);
    }
    return kept;
  }
  if (headers !== null && typeof headers === "object") {
    return Object.fromEntries(Object.entries(headers).filter(([key]) => !dropped(key)));
  }
  return headers;
}

export function newWriteHeadLookup(
  response: http.ServerResponse,
  passed: unknown,
): { get: (name: string) => string | null } {
  return {
    get: (name) => {
      const value = readWriteHeadHeader(passed, name) ?? response.getHeader(name);
      return value === undefined || value === null ? null : String(value);
    },
  };
}
