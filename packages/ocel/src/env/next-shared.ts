import type { Env } from "./access.js";
import type { Definitions, VariableDefinition } from "./definition.js";
import { EnvDefinitionError } from "./errors.js";

export const PUBLIC_PREFIX = "NEXT_PUBLIC_";

export const PUBLIC_ENV_KEY = "OCEL_PUBLIC_ENV";

export type NextVariable = Omit<VariableDefinition, "client">;

export type NextDefinitions = Record<string, NextVariable>;

type PublicKeys<T> = Extract<keyof T, `${typeof PUBLIC_PREFIX}${string}`>;

/** Every `NEXT_PUBLIC_` key must be class plain: a confidential public key is a type error. */
export type PublicPlain<T> = { readonly [K in PublicKeys<T>]: { readonly class: "plain" } };

export type NextEnv<T extends NextDefinitions> = Env<T & Definitions>;

export function isPublic(key: string): boolean {
  return key.startsWith(PUBLIC_PREFIX);
}

export function refusePublicConfidential(definitions: NextDefinitions): void {
  for (const [key, definition] of Object.entries(definitions)) {
    if (isPublic(key) && definition.class !== "plain") {
      throw new EnvDefinitionError(
        `'${key}' starts with ${PUBLIC_PREFIX}, so Next inlines it into the browser bundle, and it is class '${definition.class}'. Rename it without ${PUBLIC_PREFIX}, or declare it class 'plain'.`,
      );
    }
  }
}

export function toCore(definitions: NextDefinitions): Definitions {
  return Object.fromEntries(
    Object.entries(definitions).map(([key, definition]) => [
      key,
      isPublic(key) ? ({ ...definition, client: true } as VariableDefinition) : definition,
    ]),
  ) as Definitions;
}
