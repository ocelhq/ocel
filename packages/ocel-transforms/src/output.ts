/** The key a binding output rides under from a transform module to the deploy. */
export const outputPlaceholderKey = "$ocelOutput";

/** What the deploy resolves an output from: one property of one bound record. */
export interface BindingOutputRef {
  readonly type: string;
  readonly name: string;
  readonly property: string;
}

declare const resolvedValue: unique symbol;

/**
 * A value the deploy reads from a record your own infrastructure published, in
 * place of one a transform module could write down. It is resolved provider-side
 * against the records published to the environment being deployed, so a module
 * never holds the value itself.
 */
export interface BindingOutput<T = unknown> {
  readonly [outputPlaceholderKey]: BindingOutputRef;
  readonly [resolvedValue]?: T;
}

/** A leaf a transform may fill with either an authored value or a binding output. */
export type Linked<T> = T | BindingOutput<T>;

/** The properties one record carries, each read as a binding output. */
export type BindingProperties = { readonly [property: string]: BindingOutput<any> };

/** The records of one resource type, addressed by the name the config keys them under. */
export type BindingNames = { readonly [name: string]: BindingProperties };

/** Every readable record, addressed by resource type and then by name. */
export type BindingPlaceholders = { readonly [type: string]: BindingNames };

/**
 * The placeholders `B` describes: one property of one record per leaf, each
 * carrying the type that record publishes it as. `G` is what marks `B` as
 * written down; while nothing has augmented it — nothing has run
 * `ocel bindings generate` — every name stays open and the deploy is the check.
 */
export type BindingPlaceholdersOf<B, G> = keyof G extends never
  ? BindingPlaceholders
  : {
      readonly [T in keyof B]: {
        readonly [N in keyof B[T]]: {
          readonly [P in keyof B[T][N]]: BindingOutput<B[T][N][P]>;
        };
      };
    };

/** Whether a value names a property of a bound record. */
export function isBindingOutput(value: unknown): value is BindingOutput {
  return typeof value === "object" && value !== null && Object.hasOwn(value, outputPlaceholderKey);
}

/**
 * The records a transform module reads, one placeholder per property named.
 * Nothing is resolved here: `bindings.postgres.orders.host` is the instruction
 * the deploy carries out against the records published to the environment it
 * targets, and `bindings.custom.network.subnetIds` reads a record nothing
 * declared.
 */
export const bindings: BindingPlaceholders = new Proxy({} as BindingPlaceholders, {
  get(_target, type) {
    return unnameable(type) ? undefined : namesOf(type as string);
  },
});

function namesOf(type: string): BindingNames {
  return new Proxy({} as BindingNames, {
    get(_target, name) {
      return unnameable(name) ? undefined : propertiesOf(type, name as string);
    },
  });
}

function propertiesOf(type: string, name: string): BindingProperties {
  return new Proxy({} as BindingProperties, {
    get(_target, property) {
      return unnameable(property) ? undefined : placeholder(type, name, property as string);
    },
  });
}

function unnameable(key: string | symbol): boolean {
  return typeof key === "symbol" || key === "then";
}

function placeholder(type: string, name: string, property: string): BindingOutput {
  if (type === "") {
    throw new Error(
      "a binding output names no resource type — read it as `bindings.<type>.<name>.<property>`",
    );
  }
  if (name === "") {
    throw new Error(
      `a binding output of ${type} names no binding — name the resource your config binds, or the record your own infrastructure published under \`custom\``,
    );
  }
  if (property === "") {
    throw new Error(
      `a binding output of ${type}.${name} names no property — name the property that record carries`,
    );
  }
  return Object.freeze({
    [outputPlaceholderKey]: Object.freeze({ type, name, property }),
  });
}
