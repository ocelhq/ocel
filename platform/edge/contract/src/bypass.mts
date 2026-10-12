import { createHash, timingSafeEqual } from "node:crypto";

export const bypassCookie = "__prerender_bypass";

export const revalidateHeader = "x-prerender-revalidate";

export type Bypass = "none" | "bypass" | "revalidate";

export interface BypassRequest {
  token: string | undefined;
  method: string;
  cookie?: string | null;
  revalidate?: string | null;
}

function digest(value: string): Buffer {
  return createHash("sha256").update(value, "utf8").digest();
}

function isToken(presented: string, token: string): boolean {
  return timingSafeEqual(digest(presented), digest(token));
}

function carriesToken(cookie: string, token: string): boolean {
  let carried = false;
  for (const part of cookie.split(";")) {
    const separator = part.indexOf("=");
    if (separator === -1 || part.slice(0, separator).trim() !== bypassCookie) continue;
    carried = isToken(part.slice(separator + 1).trim(), token) || carried;
  }
  return carried;
}

export function readBypass({ token, method, cookie, revalidate }: BypassRequest): Bypass {
  if (!token) return "none";
  if (cookie && carriesToken(cookie, token)) return "bypass";
  const reads = method === "GET" || method === "HEAD";
  return reads && revalidate && isToken(revalidate, token) ? "revalidate" : "none";
}
