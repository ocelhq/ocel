import { defineEnv as defineCore } from "./edge.js";
import {
  type NextDefinitions,
  type NextEnv,
  type PublicPlain,
  refusePublicConfidential,
  toCore,
} from "./next-shared.js";

export function defineEnv<const T extends NextDefinitions>(
  definitions: T & PublicPlain<T>,
): NextEnv<T> {
  refusePublicConfidential(definitions);
  return defineCore(toCore(definitions)) as unknown as NextEnv<T>;
}
