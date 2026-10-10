import type { StandardSchemaV1 } from "@standard-schema/spec";
import { type DefinedEnvVars, defineEnvVars as defineKitEnvVars } from "@sveltejs/kit/env";
import {
  type Definitions,
  SVELTEKIT_PUBLIC_URL_KEY,
  type VariableClass,
  type VariableDefinition,
} from "./definition.js";
import { EnvDefinitionError } from "./errors.js";
import { readLiveFile } from "./file.js";
import { defineEnv } from "./index.js";

export { EnvDefinitionError } from "./errors.js";

/** A variable as `ocel/env/sveltekit` takes it: SvelteKit's own fields beside the ones ocel adds. */
export interface KitVariable {
  /** How ocel stores and delivers the value. `secret` is refused: SvelteKit reads a value once, at startup, so a rotated secret would never arrive. */
  class: VariableClass;
  /** Whether client code can read the variable. Only class `plain` can be public. */
  public?: boolean;
  /** Whether the build-time value is inlined into the bundle. A `sensitive` variable cannot be static. */
  static?: boolean;
  /** Shown on hover and in `ocel env`. */
  description?: string;
  /** The app folders that take a value of their own for this variable. */
  folders?: readonly string[];
  /** A Standard Schema, or a function returning the parsed value or throwing. */
  schema?: StandardSchemaV1<string | undefined, unknown> | ((value: string | undefined) => unknown);
}

type KitVariables = Record<string, KitVariable>;

type Refused<TKey extends string, TReason extends string> = `'${TKey}' ${TReason}`;

type Refusal<TKey extends string, TVariable> = TKey extends typeof SVELTEKIT_PUBLIC_URL_KEY
  ? {
      readonly class: Refused<
        TKey,
        "is written by ocel and is already among the variables defineEnvVars returns: read it from $app/env/public, and drop this declaration"
      >;
    }
  : TVariable extends { readonly class: "secret" }
    ? {
        readonly class: Refused<
          TKey,
          "is class secret, and SvelteKit reads a value once at startup, so a rotated secret never arrives: declare it with defineEnv from 'ocel/env', whose accessor reads it live"
        >;
      }
    : TVariable extends { readonly public: true }
      ? TVariable extends { readonly class: "plain" }
        ? unknown
        : {
            readonly class: Refused<
              TKey,
              'is public, so SvelteKit hands it to the browser: declare it class "plain", or drop public: true'
            >;
          }
      : TVariable extends { readonly static: true }
        ? TVariable extends { readonly class: "sensitive" }
          ? {
              readonly static: Refused<
                TKey,
                "is sensitive and static, so SvelteKit would inline its value into the server bundle: drop static: true"
              >;
            }
          : unknown
        : unknown;

type Refusals<TVariables extends KitVariables> = {
  readonly [K in keyof TVariables & string]: Refusal<K, TVariables[K]>;
};

type LiveFileSchema<TVariable> = TVariable extends { readonly class: "sensitive" }
  ? TVariable extends { readonly schema: unknown }
    ? unknown
    : { schema: StandardSchemaV1<string | undefined, string> }
  : unknown;

type KitFields<TVariables extends KitVariables> = {
  [K in keyof TVariables]: Omit<TVariables[K], "class" | "folders"> & LiveFileSchema<TVariables[K]>;
};

type DeploymentUrl = { public: true; schema: (value: string | undefined) => string | undefined };

/**
 * Declares the variables of a SvelteKit 3 app and hands SvelteKit its own `defineEnvVars`
 * result: export it as `variables` from `src/env.ts`. SvelteKit keeps `public`, `static`,
 * `schema` and `description`; ocel keeps `class` and `folders`, and delivers each value
 * under its own name. The result also holds `PUBLIC_OCEL_URL`, the URL the deployment is
 * served from.
 */
export function defineEnvVars<const TVariables extends KitVariables>(
  variables: TVariables & Refusals<TVariables>,
): DefinedEnvVars<KitFields<TVariables> & { PUBLIC_OCEL_URL: DeploymentUrl }> {
  refuseUnsupportedVariables(variables);

  const forKit: Record<string, Omit<KitVariable, "class" | "folders">> = {
    PUBLIC_OCEL_URL: { public: true, schema: (value) => value },
  };
  for (const [key, { class: _class, folders: _folders, ...kit }] of Object.entries(variables)) {
    forKit[key] = kit;
  }
  const normalized = defineKitEnvVars(forKit as never) as Record<
    string,
    { schema?: StandardSchemaV1<string | undefined, unknown> }
  >;

  const core: Definitions = {};
  for (const [key, variable] of Object.entries(variables as KitVariables)) {
    const schema = normalized[key]?.schema;
    core[key] = {
      class: variable.class,
      ...(variable.description === undefined ? {} : { description: variable.description }),
      ...(variable.folders === undefined ? {} : { folders: variable.folders }),
      ...(schema === undefined ? {} : { schema }),
    } as VariableDefinition;
  }
  defineEnv(core);

  for (const [key, variable] of Object.entries(variables as KitVariables)) {
    const entry = normalized[key];
    if (!entry || variable.class === "plain") continue;
    const schema = entry.schema && withholdIssues(key, entry.schema);
    entry.schema = readLiveFileWhenUnset(key, schema);
  }
  return normalized as never;
}

function refuseUnsupportedVariables(variables: KitVariables): void {
  for (const [key, variable] of Object.entries(variables)) {
    if (key === SVELTEKIT_PUBLIC_URL_KEY) {
      throw new EnvDefinitionError(
        `'${key}' is written by ocel from the URL the deployment is served from, and it is already among the variables defineEnvVars returns. Read it from $app/env/public, and drop this declaration.`,
      );
    }
    if (variable.class === "secret") {
      throw new EnvDefinitionError(
        `'${key}' is class 'secret', and SvelteKit reads a value once at startup into a module-level constant, so a rotated secret would never arrive. Declare it with defineEnv from 'ocel/env', whose accessor reads it live, or declare it class 'sensitive'.`,
      );
    }
    if (variable.public && variable.class !== "plain") {
      throw new EnvDefinitionError(
        `'${key}' is public, so SvelteKit hands it to the browser, and it is class '${variable.class}'. Drop public: true, or declare it class 'plain'.`,
      );
    }
    if (variable.static && variable.class === "sensitive") {
      throw new EnvDefinitionError(
        `'${key}' is static and class 'sensitive', so SvelteKit would inline its value into the server bundle. Drop static: true.`,
      );
    }
  }
}

function readLiveFileWhenUnset(
  key: string,
  schema: StandardSchemaV1<string | undefined, unknown> | undefined,
): StandardSchemaV1<string | undefined, unknown> {
  return {
    "~standard": {
      version: 1,
      vendor: "ocel",
      ...schema?.["~standard"],
      validate(value) {
        const delivered = (value as string | undefined) ?? readLiveFile(key);
        if (schema) return schema["~standard"].validate(delivered);
        if (delivered === undefined) {
          return { issues: [{ message: `'${key}' has no value` }] };
        }
        return { value: delivered };
      },
    },
  };
}

function withholdIssues(
  key: string,
  schema: StandardSchemaV1<string | undefined, unknown>,
): StandardSchemaV1<string | undefined, unknown> {
  return {
    "~standard": {
      ...schema["~standard"],
      validate(value) {
        const result = schema["~standard"].validate(value);
        if (result instanceof Promise || !result.issues) return result;
        return {
          issues: [
            {
              message: `withheld, because the schema message of '${key}' can quote the value itself`,
            },
          ],
        };
      },
    },
  };
}
