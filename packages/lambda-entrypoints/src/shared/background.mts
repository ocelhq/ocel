import { AsyncLocalStorage } from "node:async_hooks";

type WaitUntil = (task: Promise<unknown>) => void;

// Next loads each cache handler as its own module graph, bundled apart from the
// membrane, so a handler that wants to defer work cannot reach the invocation's
// waitUntil by import. The storage is shared through globalThis — and versioned
// — for the same reasons the tag clock's state is.
const storageKey = Symbol.for("ocel.membrane.background.v1");

const host = globalThis as Record<symbol, AsyncLocalStorage<WaitUntil> | undefined>;
const storage = (host[storageKey] ??= new AsyncLocalStorage<WaitUntil>());

// Runs `fn` with the invocation's waitUntil in scope, so anything deferred
// beneath it — however deep, and across await boundaries — lands on this
// request.
export function runWithWaitUntil<T>(waitUntil: WaitUntil, fn: () => T): T {
  return storage.run(waitUntil, fn);
}

// Defers work off the request that raised it: the membrane holds the invocation
// open until the task settles, so it still completes on a sandbox that would
// otherwise be frozen, but the response never waits for it.
//
// Nothing here may throw or reject into the caller. The callers are on paths
// Next hands through with no try/catch, where a throw would fail the very
// request the work was deferred to keep free of it.
export function background(task: () => Promise<unknown>): void {
  const deferred = Promise.resolve().then(task);
  const waitUntil = storage.getStore();
  // The drain logs and swallows a rejection. Outside an invocation there is no
  // drain and nothing holding the sandbox, so the task runs on whatever time the
  // host gives it and its rejection is swallowed here instead.
  if (waitUntil) waitUntil(deferred);
  else void deferred.catch(() => {});
}
