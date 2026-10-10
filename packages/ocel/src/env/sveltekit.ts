import type { StandardSchemaV1 } from "@standard-schema/spec";
import { type DefinedEnvVars, defineEnvVars as defineKit } from "@sveltejs/kit/env";
import type { Definitions, VariableClass, VariableDefinition } from "./definition.js";
import { EnvDefinitionError } from "./errors.js";
import { defineEnv as defineCore } from "./index.js";

export interface KitVariable {
  class: VariableClass;
  public?: boolean;
  static?: boolean;
  description?: string;
  folders?: readonly string[];
  schema?: StandardSchemaV1<string | undefined, unknown> | ((value: string | undefined) => unknown);
}

type KitVariables = Record<string, KitVariable>;

/** A `public: true` variable must be class plain: a confidential public one is a type error. */
type PublicPlain<T> = {
  readonly [K in keyof T]: T[K] extends { public: true } ? { readonly class: "plain" } : unknown;
};

type KitOnly<T extends KitVariables> = {
  [K in keyof T]: Omit<T[K], "class" | "folders">;
};

export function defineEnvVars<const T extends KitVariables>(
  variables: T & PublicPlain<T>,
): DefinedEnvVars<KitOnly<T>> {
  const forKit: Record<string, Omit<KitVariable, "class" | "folders">> = {};
  for (const [key, { class: variableClass, folders: _folders, ...rest }] of Object.entries(
    variables as KitVariables,
  )) {
    if (rest.public && variableClass !== "plain") {
      throw new EnvDefinitionError(
        `'${key}' is public, so SvelteKit hands it to the browser, and it is class '${variableClass}'. Drop public: true, or declare it class 'plain'.`,
      );
    }
    forKit[key] = rest;
  }

  // Kit turns a function schema into a standard schema; core reads only standard schemas.
  const normalized = defineKit(forKit as never) as Record<string, { schema?: StandardSchemaV1 }>;

  const core: Definitions = {};
  for (const [key, variable] of Object.entries(variables as KitVariables)) {
    core[key] = {
      class: variable.class,
      ...(variable.public ? { client: true } : {}),
      ...(variable.description === undefined ? {} : { description: variable.description }),
      ...(variable.folders === undefined ? {} : { folders: variable.folders }),
      ...(normalized[key]?.schema === undefined ? {} : { schema: normalized[key].schema }),
    } as VariableDefinition;
  }
  defineCore(core);

  return normalized as unknown as DefinedEnvVars<KitOnly<T>>;
}
