import assert from "node:assert/strict";
import { setTimeout as delay } from "node:timers/promises";
import type { json } from "./context";

export const SETTLED_WITHIN_MS = 60_000;
export const POLL_MS = 250;

export const ENDED = ["COMPLETED", "FAILED", "CANCELED", "EXPIRED", "TIMED_OUT"];

export type Reply = Awaited<ReturnType<typeof json>>;

export type RunRecord = {
  id: string;
  task: string;
  status: string;
  payload: unknown;
  output: unknown;
  error?: string;
  attempts: number;
  tags: string[];
  createdAt?: number;
  dueAt?: number;
  startedAt?: number;
  finishedAt?: number;
  expiresAt?: number;
};

export function describeReply(reply: Reply): string {
  return `${reply.res.status} ${reply.text}`;
}

export function readAnswer<T>(reply: Reply, what: string): T {
  assert.equal(reply.res.status, 200, `${what} answered ${describeReply(reply)}`);
  return reply.body as T;
}

export async function waitFor<T>(
  what: string | (() => string),
  read: () => Promise<T | undefined>,
  withinMs = SETTLED_WITHIN_MS,
): Promise<T> {
  const deadline = Date.now() + withinMs;
  for (;;) {
    const found = await read();
    if (found !== undefined) {
      return found;
    }
    assert.ok(
      Date.now() < deadline,
      typeof what === "string" ? `${what} within ${withinMs}ms` : what(),
    );
    await delay(POLL_MS);
  }
}
