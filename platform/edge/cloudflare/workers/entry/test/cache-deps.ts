import type { CacheDeps } from "../src/cache";

export function coloDeps(deps: CacheDeps): CacheDeps {
  return { admissionDelay: () => Promise.resolve(), ...deps };
}
