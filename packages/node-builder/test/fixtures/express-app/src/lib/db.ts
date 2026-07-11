import { greeting } from "../greeting.js";

// Relative import WITH extension above stays untouched; this module is reached
// via an extensionless import from server.ts that must be rewritten.
export function render(name: string): string {
  return greeting(name).toUpperCase();
}
