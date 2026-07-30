import { greeting } from "../greeting.js";
import { mark } from "typed-dep";

// This module HAS type annotations. nft's parser chokes on typed TS, so a dep
// reached only through this file (`typed-dep`) is missing unless the builder
// feeds nft transpiled JS for analysis. The `../greeting.js` import (already
// extensioned) stays untouched.
export function render(name: string): string {
  return mark(greeting(name).toUpperCase());
}
