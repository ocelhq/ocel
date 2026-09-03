import assert from "node:assert/strict";
import type { ContractContext } from "./contract";

const POLL_MS = 1000;
const STATE_POLL_MS = 250;

export type StateRow = {
  key: string;
  count: number;
  firstSeen: string;
  lastSeen: string;
};

export async function page(
  ctx: ContractContext,
  path: string,
  init?: RequestInit,
): Promise<{ res: Response; html: string }> {
  const res = await ctx.fetch(`${ctx.baseUrl}${path}`, init);
  assert.equal(res.status, 200, `${path} answered ${res.status}`);
  return { res, html: await res.text() };
}

export async function pageHtml(ctx: ContractContext, path: string): Promise<string> {
  return (await page(ctx, path)).html;
}

export async function text(ctx: ContractContext, path: string, init?: RequestInit) {
  const res = await ctx.fetch(`${ctx.baseUrl}${path}`, init);
  return { res, body: await res.text() };
}

export async function state(
  ctx: ContractContext,
  keys: string[],
): Promise<Map<string, StateRow>> {
  const asked = keys.map((key) => `key=${encodeURIComponent(key)}`).join("&");
  const res = await ctx.fetch(`${ctx.baseUrl}/api/next/state?${asked}`);
  assert.equal(res.status, 200, "the state readback answered " + res.status);
  const read = (await res.json()) as { rows: StateRow[] };
  return new Map(read.rows.map((row) => [row.key, row]));
}

export async function stateRow(
  ctx: ContractContext,
  key: string,
  timeoutMs: number,
): Promise<StateRow> {
  return until(
    timeoutMs,
    `${key} never reached the state readback`,
    async () => (await state(ctx, [key])).get(key),
    STATE_POLL_MS,
  );
}

export async function steady(
  read: () => Promise<string>,
  what: string,
  reads: number,
  attempts: number,
): Promise<string> {
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    const first = await read();
    const rest: string[] = [];
    for (let more = 1; more < reads; more += 1) {
      rest.push(await read());
    }
    if (rest.every((seen) => seen === first)) {
      return first;
    }
  }
  return assert.fail(`${what} moved between two of ${reads} reads on each of ${attempts} attempts`);
}

export async function until<T>(
  timeoutMs: number,
  what: string,
  read: () => Promise<T | undefined>,
  pollMs = POLL_MS,
): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const found = await read();
    if (found !== undefined) {
      return found;
    }
    assert.ok(Date.now() < deadline, what);
    await new Promise((resolve) => setTimeout(resolve, pollMs));
  }
}
