import { useSyncExternalStore } from "react";

let version = 0;
const listeners = new Set<() => void>();

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export interface Readable<T> {
  readonly value: T;
}

export class Signal<T> implements Readable<T> {
  #value: T;

  constructor(initial: T) {
    this.#value = initial;
  }

  get value(): T {
    return this.#value;
  }

  set value(next: T) {
    if (Object.is(this.#value, next)) return;
    this.#value = next;
    version += 1;
    for (const listener of [...listeners]) listener();
  }
}

class Computed<T> implements Readable<T> {
  #compute: () => T;
  #cached: T | undefined;
  #at = -1;

  constructor(compute: () => T) {
    this.#compute = compute;
  }

  get value(): T {
    if (this.#at !== version) {
      this.#cached = this.#compute();
      this.#at = version;
    }
    return this.#cached as T;
  }
}

export function signal<T>(initial: T): Signal<T> {
  return new Signal(initial);
}

export function computed<T>(compute: () => T): Readable<T> {
  return new Computed(compute);
}

export function useValue<T>(source: Readable<T>): T {
  return useSyncExternalStore(subscribe, () => source.value);
}
