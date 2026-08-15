/** The key a link output rides under from a transform module to the deploy. */
export const outputPlaceholderKey = "$ocelOutput";

/** What the deploy resolves an output from: one property of one published record. */
export interface LinkOutputRef {
  readonly link: string;
  readonly property: string;
  readonly list?: true;
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

/** Reads one property of a published link record as a string. */
export function output(link: string, property: string): LinkOutput {
  return placeholder(link, property, false);
}

/**
 * Reads one property of a published link record as a list, splitting the
 * published string on commas.
 */
export function outputList(link: string, property: string): LinkOutput {
  return placeholder(link, property, true);
}

/** Whether a value was authored by `output` or `outputList`. */
export function isLinkOutput(value: unknown): value is LinkOutput {
  return (
    typeof value === "object" &&
    value !== null &&
    Object.hasOwn(value, outputPlaceholderKey)
  );
}

function placeholder(
  link: string,
  property: string,
  list: boolean,
): LinkOutput {
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
  const ref: LinkOutputRef = list
    ? { link, property, list: true }
    : { link, property };
  return Object.freeze({ [outputPlaceholderKey]: Object.freeze(ref) });
}
