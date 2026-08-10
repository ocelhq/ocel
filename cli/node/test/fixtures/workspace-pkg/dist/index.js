import { brand } from "./brand";

// Reached only via the package's `exports` map; `./brand` is extensionless,
// like the ocel SDK's tsc dist. Exercises both defects at once: placement by
// package identity (real files live outside any node_modules) and the ESM
// ext-rewrite inside a workspace package.
export function label(value) {
  return brand(value);
}
