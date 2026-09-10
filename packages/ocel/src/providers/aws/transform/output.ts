/** The key a binding output rides under from a transform module to the deploy. */
export const outputPlaceholderKey = "$ocelOutput";

/** What the deploy resolves an output from: one property of one published record. */
export interface BindingOutputRef {
  readonly binding: string;
  readonly property: string;
}

declare const resolvedValue: unique symbol;

/**
 * A value the deploy reads from a binding your own infrastructure published, in
 * place of one a transform module could write down. It is resolved provider-side
 * against the records published to the environment being deployed, so a module
 * never holds the value itself. `T` is what the record carries under that
 * property, known only once `ocel bindings generate` has written it down.
 */
export interface BindingOutput<T = unknown> {
  readonly [outputPlaceholderKey]: BindingOutputRef;
  readonly [resolvedValue]?: T;
}

/** A field a transform may fill with either an authored value or a binding output. */
export type Bound<T> = T | BindingOutput<T>;

/** The properties one published record carries, each read as a binding output. */
export type BindingProperties = { readonly [property: string]: BindingOutput<any> };

/** Every published record, addressed by the name it was published under. */
export type BindingPlaceholders = { readonly [binding: string]: BindingProperties };

/**
 * The placeholders `L` describes: one property of one record per field, each
 * carrying the type that record publishes it as. `G` is what marks `L` as
 * written down; while nothing has augmented it — nothing has run
 * `ocel bindings generate` — every name stays open and the deploy is the check.
 */
export type BindingPlaceholdersOf<L, G> = keyof G extends never
  ? BindingPlaceholders
  : {
      readonly [K in keyof L]: {
        readonly [P in keyof L[K]]: BindingOutput<L[K][P]>;
      };
    };

/** Whether a value names a property of a published record. */
export function isBindingOutput(value: unknown): value is BindingOutput {
  return typeof value === "object" && value !== null && Object.hasOwn(value, outputPlaceholderKey);
}

/**
 * The published records a callback transform reads, one placeholder per
 * property named. Nothing is resolved here: `bindings.orders.host` is the
 * instruction the deploy carries out against the records published to the
 * environment it targets.
 */
export const bindings: BindingPlaceholders = new Proxy({} as BindingPlaceholders, {
  get(_target, binding) {
    return unnameable(binding) ? undefined : propertiesOf(binding as string);
  },
});

function propertiesOf(binding: string): BindingProperties {
  return new Proxy({} as BindingProperties, {
    get(_target, property) {
      return unnameable(property) ? undefined : placeholder(binding, property as string);
    },
  });
}

function unnameable(key: string | symbol): boolean {
  return typeof key === "symbol" || key === "then";
}

function placeholder(binding: string, property: string): BindingOutput {
  if (binding === "") {
    throw new Error(
      "a binding output names no binding — name the record your own infrastructure publishes",
    );
  }
  if (property === "") {
    throw new Error(
      `a binding output of ${binding} names no property — name the property that record carries`,
    );
  }
  return Object.freeze({
    [outputPlaceholderKey]: Object.freeze({ binding, property }),
  });
}
