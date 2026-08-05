// Every CacheDeps the suite builds goes through here, and that is the whole
// point of it existing: the production default for admissionDelay is a real
// uniform draw from [0, admissionJitterMs), so an inline `{ cache, waitUntil }`
// makes each background refresh sleep up to a second of wall clock, redrawn
// every run. That is seconds of pure sleep spread over the suite, every
// duration non-deterministic, and tests within reach of vitest's default
// timeout on a value that changes between runs — a flake that reads as a cache
// bug. One constructor, so a new deps object cannot quietly reacquire it.
//
// The default is left in place in exactly one test ("draws its own delay when
// none is injected"), which is what proves the seam has not been left wired in
// production.
import type { CacheDeps } from "../src/cache";

export function coloDeps(deps: CacheDeps): CacheDeps {
  return { admissionDelay: () => Promise.resolve(), ...deps };
}
