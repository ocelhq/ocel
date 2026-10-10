import { type Access, type Env, envAccessor, FIXED } from "./access.js";
import {
  type Definitions,
  type EnvDefinitions,
  flattenDefinitions,
  type GroupDefinition,
  type VariableDefinition,
} from "./definition.js";
import { EnvClientError, EnvDefinitionError } from "./errors.js";
import { assertInScope } from "./scope.js";
import { coerce } from "./value.js";

const NEXT_PUBLIC_PREFIX = "NEXT_PUBLIC_";

type NextPublicKey = `${typeof NEXT_PUBLIC_PREFIX}${string}`;

type RefusedClass<TKey extends string> =
  `'${TKey}' starts with NEXT_PUBLIC_, so Next inlines it into the browser bundle: declare it class "plain", or rename it without the prefix`;

type PlainWhenNextPublic<TKey extends string, TDefinition> = TKey extends NextPublicKey
  ? TDefinition extends { readonly class: "plain" }
    ? unknown
    : { readonly class: RefusedClass<TKey> }
  : unknown;

type NextPublicMembersPlain<TMembers extends Definitions> = {
  readonly [K in keyof TMembers & string]: PlainWhenNextPublic<K, TMembers[K]>;
};

export type PublicPlain<TDefinitions extends EnvDefinitions> = {
  readonly [K in keyof TDefinitions & string]: TDefinitions[K] extends GroupDefinition<
    infer TMembers,
    boolean
  >
    ? { readonly definitions: NextPublicMembersPlain<TMembers> }
    : PlainWhenNextPublic<K, TDefinitions[K]>;
};

function isNextPublic(key: string): boolean {
  return key.startsWith(NEXT_PUBLIC_PREFIX);
}

export function refusePublicConfidential(definitions: EnvDefinitions): void {
  for (const [key, definition] of Object.entries(flattenDefinitions(definitions))) {
    if (isNextPublic(key) && definition.class !== "plain") {
      throw new EnvDefinitionError(
        `'${key}' starts with ${NEXT_PUBLIC_PREFIX}, so Next inlines it into the browser bundle, and it is class '${definition.class}'. Declare it class 'plain', or rename it without ${NEXT_PUBLIC_PREFIX}.`,
      );
    }
  }
}

let parsed: { raw: string; values: Record<string, string> } | undefined;

function findInlinedValues(): Record<string, string> | undefined {
  const raw = process.env.OCEL_PUBLIC_ENV;
  if (typeof raw !== "string") return undefined;
  if (parsed?.raw !== raw) parsed = { raw, values: JSON.parse(raw) as Record<string, string> };
  return parsed.values;
}

function readInlinedValues(): Record<string, string> {
  const values = findInlinedValues();
  if (values === undefined) {
    throw new EnvClientError(
      "no public variables were inlined into this bundle: the build ran without ocel's Next adapter, which defines them. Build with `ocel build` or `ocel deploy`, and run the dev server with `ocel dev`.",
    );
  }
  return values;
}

function newServerOnlyError(key: string): EnvClientError {
  return new EnvClientError(
    `'${key}' is server-only and cannot be read in the browser: only variables starting with ${NEXT_PUBLIC_PREFIX} reach client code. Read '${key}' in a server component, route handler or server action.`,
  );
}

export function defineInlinedEnv<const TDefinitions extends EnvDefinitions>(
  definitions: TDefinitions,
): Env<TDefinitions> {
  return envAccessor(definitions, {
    resolve(key: string, definition: VariableDefinition) {
      if (!isNextPublic(key)) throw newServerOnlyError(key);
      return coerce(key, definition, readInlinedValues()[key]);
    },
    delivered(key: string) {
      if (!isNextPublic(key)) throw newServerOnlyError(key);
      return readInlinedValues()[key] !== undefined;
    },
    generationOf: () => FIXED,
  });
}

export function withInlinedPublicValues(access: Access): Access {
  return {
    resolve(key, definition) {
      const inlined = findInlinedValues();
      if (!isNextPublic(key) || inlined === undefined) return access.resolve(key, definition);
      assertInScope(key, definition.folders ?? []);
      return coerce(key, definition, inlined[key]);
    },
    delivered(key, definition) {
      const inlined = findInlinedValues();
      if (!isNextPublic(key) || inlined === undefined) return access.delivered(key, definition);
      return inlined[key] !== undefined;
    },
    generationOf: (definition) => access.generationOf(definition),
  };
}
