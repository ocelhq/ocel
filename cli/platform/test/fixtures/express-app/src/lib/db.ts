import { greeting } from "../greeting.js";
import { mark } from "typed-dep";

export function render(name: string): string {
  return mark(greeting(name).toUpperCase());
}
