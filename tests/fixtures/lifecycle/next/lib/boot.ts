import { randomUUID } from "node:crypto";

const KEY = Symbol.for("ocel.next.boot");

type Booted = { [KEY]?: string };

export function bootId(): string {
  const booted = globalThis as Booted;
  booted[KEY] ??= randomUUID();
  return booted[KEY];
}
