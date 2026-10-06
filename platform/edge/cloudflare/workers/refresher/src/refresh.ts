import { parseMessage, type RevalidationMessage } from "@platform/edge-contract/revalidation";

export const triggerTimeoutMs = 10_000;

export type RefreshOutcome =
  | { settle: "ack"; event: "RevalidateOk" | "RevalidateExpectMiss" }
  | { settle: "ack"; event: "RevalidateFailed"; reason: "malformed" | "unsupported-version" }
  | { settle: "ack"; event: "RevalidateFailed"; reason: "origin-unusable" }
  | {
      settle: "retry";
      event: "RevalidateFailed";
      reason: "origin-unconfigured" | "timeout" | "fetch-failed" | "status-not-ok";
      status?: number;
    };

export interface Refresh {
  message: RevalidationMessage;
  url: string;
  headers: Headers;
}

export type ParsedRefresh = { ok: true; refresh: Refresh } | { ok: false; outcome: RefreshOutcome };

function composeOriginUrl(origin: unknown, routePath: string): string | undefined {
  if (typeof origin !== "string") return undefined;
  try {
    const base = new URL(origin);
    const url = new URL(routePath, base);
    if (base.protocol !== "https:" || url.origin !== base.origin) return undefined;
    return url.href;
  } catch {
    return undefined;
  }
}

export function parseRefresh(body: unknown): ParsedRefresh {
  if (typeof body !== "string") {
    return {
      ok: false,
      outcome: { settle: "ack", event: "RevalidateFailed", reason: "malformed" },
    };
  }
  const parsed = parseMessage(body);
  if (!parsed.ok) {
    return {
      ok: false,
      outcome: { settle: "ack", event: "RevalidateFailed", reason: parsed.reason },
    };
  }
  const url = composeOriginUrl(
    (JSON.parse(body) as { origin?: unknown }).origin,
    parsed.message.routePath,
  );
  if (url === undefined) {
    return {
      ok: false,
      outcome: { settle: "ack", event: "RevalidateFailed", reason: "origin-unusable" },
    };
  }
  let headers: Headers;
  try {
    headers = new Headers(parsed.message.headers);
  } catch {
    return {
      ok: false,
      outcome: { settle: "ack", event: "RevalidateFailed", reason: "malformed" },
    };
  }
  return { ok: true, refresh: { message: parsed.message, url, headers } };
}

export async function trigger(
  refresh: Refresh,
  origin: Fetcher | undefined,
): Promise<RefreshOutcome> {
  if (origin === undefined) {
    return {
      settle: "retry",
      event: "RevalidateFailed",
      reason: "origin-unconfigured",
    };
  }
  const signal = AbortSignal.timeout(triggerTimeoutMs);
  let response: Response;
  try {
    response = await origin.fetch(refresh.url, {
      method: "HEAD",
      headers: refresh.headers,
      redirect: "manual",
      signal,
    });
  } catch {
    return {
      settle: "retry",
      event: "RevalidateFailed",
      reason: signal.aborted ? "timeout" : "fetch-failed",
    };
  }
  if (!response.ok) {
    return {
      settle: "retry",
      event: "RevalidateFailed",
      reason: "status-not-ok",
      status: response.status,
    };
  }
  const { expect } = refresh.message;
  if (expect === null || response.headers.get(expect.header) === expect.value) {
    return { settle: "ack", event: "RevalidateOk" };
  }
  return { settle: "ack", event: "RevalidateExpectMiss" };
}
