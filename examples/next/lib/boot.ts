import { randomUUID } from "node:crypto";

const KEY = "OCEL_NEXT_BOOT";

export function bootId(): string {
  const seen = process.env[KEY];
  if (seen) {
    return seen;
  }
  const fresh = randomUUID();
  process.env[KEY] = fresh;
  return fresh;
}
