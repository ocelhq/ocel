import { AsyncLocalStorage } from "node:async_hooks";

type WaitUntil = (task: Promise<unknown>) => void;

const storageKey = Symbol.for("ocel.membrane.background.v1");

const host = globalThis as Record<symbol, AsyncLocalStorage<WaitUntil> | undefined>;
const storage = (host[storageKey] ??= new AsyncLocalStorage<WaitUntil>());

export function runWithWaitUntil<T>(waitUntil: WaitUntil, fn: () => T): T {
  return storage.run(waitUntil, fn);
}

export function background(task: () => Promise<unknown>): void {
  const deferred = Promise.resolve().then(task);
  const waitUntil = storage.getStore();
  if (waitUntil) waitUntil(deferred);
  else void deferred.catch(() => {});
}
