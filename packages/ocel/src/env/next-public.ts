import { type Env, envAccessor, FIXED } from "./access.js";
import {
  type Definitions,
  type EnvDefinitions,
  flattenDefinitions,
  type GroupDefinition,
  type VariableDefinition,
} from "./definition.js";
import { EnvClientError, EnvDefinitionError } from "./errors.js";
import { coerce } from "./value.js";

export const PUBLIC_PREFIX = "NEXT_PUBLIC_";

type PublicKey = `${typeof PUBLIC_PREFIX}${string}`;

type RefusedClass<TKey extends string> =
  `'${TKey}' starts with NEXT_PUBLIC_, so Next inlines it into the browser bundle: declare it class "plain", or rename it without the prefix`;

type PlainWhenPublic<TKey extends string, TDefinition> = TKey extends PublicKey
  ? TDefinition extends { readonly class: "plain" }
    ? unknown
    : { readonly class: RefusedClass<TKey> }
  : unknown;

type PublicMembersPlain<TMembers extends Definitions> = {
  readonly [K in keyof TMembers & string]: PlainWhenPublic<K, TMembers[K]>;
};

export type PublicPlain<TDefinitions extends EnvDefinitions> = {
  readonly [K in keyof TDefinitions & string]: TDefinitions[K] extends GroupDefinition<
    infer TMembers,
    boolean
  >
    ? { readonly definitions: PublicMembersPlain<TMembers> }
    : PlainWhenPublic<K, TDefinitions[K]>;
};

export function isPublic(key: string): boolean {
  return key.startsWith(PUBLIC_PREFIX);
}

export function refusePublicConfidential(definitions: EnvDefinitions): void {
  for (const [key, definition] of Object.entries(flattenDefinitions(definitions))) {
    if (isPublic(key) && definition.class !== "plain") {
      throw new EnvDefinitionError(
        `'${key}' starts with ${PUBLIC_PREFIX}, so Next inlines it into the browser bundle, and it is class '${definition.class}'. Declare it class 'plain', or rename it without ${PUBLIC_PREFIX}.`,
      );
    }
  }
}

export function inlinedValues(): Record<string, string> | undefined {
  const raw = process.env.OCEL_PUBLIC_ENV;
  if (typeof raw !== "string") return undefined;
  return JSON.parse(raw) as Record<string, string>;
}

function inlinedOrRefuse(): Record<string, string> {
  const values = inlinedValues();
  if (values === undefined) {
    throw new EnvClientError(
      "no public variables were inlined into this bundle: the build ran without ocel's Next adapter, which defines them. Build with `ocel build` or `ocel deploy`, and run the dev server with `ocel dev`.",
    );
  }
  return values;
}

function serverOnly(key: string): EnvClientError {
  return new EnvClientError(
    `'${key}' is server-only and cannot be read in the browser: only variables starting with ${PUBLIC_PREFIX} reach client code. Read '${key}' in a server component, route handler or server action.`,
  );
}

export function inlinedEnv<const TDefinitions extends EnvDefinitions>(
  definitions: TDefinitions,
): Env<TDefinitions> {
  return envAccessor(definitions, {
    resolve(key: string, definition: VariableDefinition) {
      if (!isPublic(key)) throw serverOnly(key);
      return coerce(key, definition, inlinedOrRefuse()[key]);
    },
    delivered(key: string) {
      if (!isPublic(key)) throw serverOnly(key);
      return inlinedOrRefuse()[key] !== undefined;
    },
    generationOf: () => FIXED,
  });
}

function isInlined(definitions: EnvDefinitions, name: string): boolean {
  const definition = definitions[name];
  if (!definition) return false;
  if ("definitions" in definition) return Object.keys(definition.definitions).every(isPublic);
  return isPublic(name);
}

export function overlayInlined<const TDefinitions extends EnvDefinitions>(
  definitions: TDefinitions,
  server: Env<TDefinitions>,
): Env<TDefinitions> {
  const inlined = inlinedEnv(definitions);
  return new Proxy(server, {
    get(target, property, receiver) {
      if (
        typeof property === "string" &&
        isInlined(definitions, property) &&
        inlinedValues() !== undefined
      ) {
        return inlined[property as keyof TDefinitions];
      }
      return Reflect.get(target, property, receiver);
    },
  });
}
