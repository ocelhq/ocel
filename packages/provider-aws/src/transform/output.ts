/** The key a link output rides under from a transform module to the deploy. */
export const outputPlaceholderKey = "$ocelOutput";

/** What the deploy resolves an output from: one property of one published record. */
export interface LinkOutputRef {
  readonly link: string;
  readonly property: string;
}

declare const resolvedValue: unique symbol;

/**
 * A value the deploy reads from a link your own infrastructure published, in
 * place of one a transform module could write down. It is resolved provider-side
 * against the records published to the environment being deployed, so a module
 * never holds the value itself. `T` is what the record carries under that
 * property, known only once `ocel link generate` has written it down.
 */
export interface LinkOutput<T = unknown> {
  readonly [outputPlaceholderKey]: LinkOutputRef;
  readonly [resolvedValue]?: T;
}

/** A field a transform may fill with either an authored value or a link output. */
export type Linked<T> = T | LinkOutput<T>;

/** The properties one published record carries, each read as a link output. */
export type LinkProperties = { readonly [property: string]: LinkOutput<any> };

/** Every published record, addressed by the name it was published under. */
export type LinkPlaceholders = { readonly [link: string]: LinkProperties };

/**
 * The placeholders `L` describes: one property of one record per field, each
 * carrying the type that record publishes it as. `G` is what marks `L` as
 * written down; while nothing has augmented it — nothing has run
 * `ocel link generate` — every name stays open and the deploy is the check.
 */
export type LinkPlaceholdersOf<L, G> = keyof G extends never
  ? LinkPlaceholders
  : {
      readonly [K in keyof L]: {
        readonly [P in keyof L[K]]: LinkOutput<L[K][P]>;
      };
    };

/** Whether a value names a property of a published record. */
export function isLinkOutput(value: unknown): value is LinkOutput {
  return (
    typeof value === "object" &&
    value !== null &&
    Object.hasOwn(value, outputPlaceholderKey)
  );
}

/**
 * The published records a callback transform reads, one placeholder per
 * property named. Nothing is resolved here: `links.orders.host` is the
 * instruction the deploy carries out against the records published to the
 * environment it targets.
 */
export const links: LinkPlaceholders = new Proxy({} as LinkPlaceholders, {
  get(_target, link) {
    return unnameable(link) ? undefined : propertiesOf(link as string);
  },
});

function propertiesOf(link: string): LinkProperties {
  return new Proxy({} as LinkProperties, {
    get(_target, property) {
      return unnameable(property)
        ? undefined
        : placeholder(link, property as string);
    },
  });
}

function unnameable(key: string | symbol): boolean {
  return typeof key === "symbol" || key === "then";
}

function placeholder(link: string, property: string): LinkOutput {
  if (link === "") {
    throw new Error(
      "a link output names no link — name the record your own infrastructure publishes",
    );
  }
  if (property === "") {
    throw new Error(
      `a link output of ${link} names no property — name the property that record carries`,
    );
  }
  return Object.freeze({
    [outputPlaceholderKey]: Object.freeze({ link, property }),
  });
}
