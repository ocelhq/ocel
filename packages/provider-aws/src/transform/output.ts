/** The key a link output rides under from a transform module to the deploy. */
export const outputPlaceholderKey = "$ocelOutput";

/** What the deploy resolves an output from: one property of one published record. */
export interface LinkOutputRef {
  readonly link: string;
  readonly property: string;
}

/**
 * A value the deploy reads from a link your own infrastructure published, in
 * place of one a transform module could write down. It is resolved provider-side
 * against the records published to the environment being deployed, so a module
 * never holds the value itself.
 */
export interface LinkOutput {
  readonly [outputPlaceholderKey]: LinkOutputRef;
}

/** A field a transform may fill with either an authored value or a link output. */
export type Linked<T> = T | LinkOutput;

/** The properties one published record carries, each read as a link output. */
export type LinkProperties = { readonly [property: string]: LinkOutput };

/** Every published record, addressed by the name it was published under. */
export type LinkPlaceholders = { readonly [link: string]: LinkProperties };

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
