import type { StandardSchemaV1 } from "@standard-schema/spec";
import type { EnvDefinitions, GroupDefinition, VariableDefinition } from "./definition.js";
import { undeclared } from "./value.js";

type EnvValue<T> =
  T extends GroupDefinition<infer TDefinitions, infer TOptional>
    ? Env<TDefinitions> | (TOptional extends true ? undefined : never)
    : T extends VariableDefinition
      ? T["schema"] extends StandardSchemaV1
        ? StandardSchemaV1.InferOutput<T["schema"]>
        : string
      : never;

/** The object a `defineEnv` call hands back: one property per declared variable or group. */
export type Env<TDefinitions extends EnvDefinitions> = {
  readonly [K in keyof TDefinitions]: EnvValue<TDefinitions[K]>;
};

/** What a tier supplies so the shared accessor can read its variables. */
export interface Access {
  resolve(key: string, definition: VariableDefinition): unknown;
  delivered(key: string, definition: VariableDefinition): boolean;
  generationOf(definition: VariableDefinition): number;
}

export const FIXED = -1;

export function envAccessor<TDefinitions extends EnvDefinitions>(
  definitions: TDefinitions,
  access: Access,
): Env<TDefinitions> {
  const resolved = new Map<string, { generation: number; value: unknown }>();

  return new Proxy({} as Env<TDefinitions>, {
    get(_target, property) {
      if (typeof property === "symbol") return undefined;

      const key = property;
      const definition = definitions[key];
      if (!definition) throw undeclared(key);

      const generation = generationOf(definition, access);
      const memo = resolved.get(key);
      if (memo?.generation === generation) return memo.value;

      const value =
        "definitions" in definition
          ? resolveGroup(definition, access)
          : access.resolve(key, definition);
      resolved.set(key, { generation, value });
      return value;
    },
  });
}

function generationOf(definition: VariableDefinition | GroupDefinition, access: Access): number {
  if (!("definitions" in definition)) return access.generationOf(definition);
  let generation = FIXED;
  for (const member of Object.values(definition.definitions)) {
    generation = Math.max(generation, access.generationOf(member));
  }
  return generation;
}

function resolveGroup(
  definition: GroupDefinition,
  access: Access,
): Record<string, unknown> | undefined {
  const members = Object.entries(definition.definitions);
  if (definition.optional && !members.some(([key, member]) => access.delivered(key, member))) {
    return undefined;
  }

  const value: Record<string, unknown> = {};
  for (const [key, member] of members) value[key] = access.resolve(key, member);
  return value;
}
