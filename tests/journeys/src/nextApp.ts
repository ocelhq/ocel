import assert from "node:assert/strict";
import type { ContractContext } from "./contract";

export type StateRow = {
  key: string;
  count: number;
  firstSeen: string;
  lastSeen: string;
};

export async function pageHtml(ctx: ContractContext, path: string): Promise<string> {
  const res = await ctx.fetch(`${ctx.baseUrl}${path}`);
  assert.equal(res.status, 200, `${path} answered ${res.status}`);
  return res.text();
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
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const row = (await state(ctx, [key])).get(key);
    if (row) {
      return row;
    }
    assert.ok(Date.now() < deadline, `${key} never reached the state readback`);
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
}

export async function until<T>(
  timeoutMs: number,
  what: string,
  read: () => Promise<T | undefined>,
): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const found = await read();
    if (found !== undefined) {
      return found;
    }
    assert.ok(Date.now() < deadline, what);
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }
}
