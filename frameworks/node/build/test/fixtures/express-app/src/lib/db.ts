import { mark } from "typed-dep";
import { greeting } from "../greeting.js";

export function render(name: string): string {
  return mark(greeting(name).toUpperCase());
}
