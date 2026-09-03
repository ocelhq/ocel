import type { StandardSchemaV1 } from "@standard-schema/spec";
import { callSite } from "./callsite.js";
import { EnvClientError } from "./client.js";
import type { Definitions } from "./definition.js";
import { parse } from "./standard.js";

const SOURCE: unique symbol = Symbol.for("ocel.env.schema");

export type EnvSchema<TDefinitions extends Definitions> = TDefinitions & {
  readonly [SOURCE]: string;
};

export function envSchema<const TDefinitions extends Definitions>(
  definitions: TDefinitions,
): EnvSchema<TDefinitions> {
  return Object.defineProperty(definitions, SOURCE, {
    value: callSite(),
    enumerable: false,
  }) as EnvSchema<TDefinitions>;
}

export function sourceOf(definitions: Definitions): string {
  const source = (definitions as { [SOURCE]?: unknown })[SOURCE];
  return typeof source === "string" ? source : "";
}

export type Declared<TModule> = Extract<
  TModule[keyof TModule],
  { readonly [SOURCE]: string }
>;

type Holding<TModule> = [Declared<TModule>] extends [never]
  ? "this module exports nothing made by envSchema()"
  : unknown;

export function declared<TModule extends object>(
  module: TModule & Holding<TModule>,
): Declared<TModule> {
  for (const exported of Object.values(module)) {
    if (exported !== null && typeof exported === "object" && SOURCE in exported) {
      return exported as Declared<TModule>;
    }
  }
  throw new EnvClientError(
    "The module the client accessor imports its schemas from exports nothing made by envSchema(). Declare the variables with envSchema from 'ocel/env/schema' in a module that does not call defineEnv, and hand that to defineEnv.",
  );
}

export type ClientValue<
  TSchema extends Definitions,
  TKey extends string,
> = TKey extends keyof TSchema
  ? TSchema[TKey]["schema"] extends StandardSchemaV1
    ? StandardSchemaV1.InferOutput<TSchema[TKey]["schema"]>
    : string
  : string;

export function inlined<TSchema extends Definitions, TKey extends string>(
  schema: TSchema,
  key: TKey,
  value: string | undefined,
): ClientValue<TSchema, TKey> {
  if (value === undefined) {
    throw new EnvClientError(
      `'${key}' is client-accessible, but no value was inlined into this bundle. A bundler inlines only the names matching its own public convention (NEXT_PUBLIC_* under Next, VITE_* under Vite); rename the variable to one of those, or read it on the server instead.`,
    );
  }
  const definition = schema[key];
  if (!definition?.schema) return value as ClientValue<TSchema, TKey>;
  const result = parse(definition.schema, value);
  if (!result.ok) {
    throw new EnvClientError(
      `'${key}' is set but does not satisfy its schema: ${result.message}. Fix it with \`ocel env set ${key} <VALUE>\`.`,
    );
  }
  return result.value as ClientValue<TSchema, TKey>;
}
