import { source } from "./cli.js";

/** One `links.v1.Link` holding a custom record's open properties, ready for protobuf JSON. */
export interface CustomLink {
  name: string;
  custom: Record<string, unknown>;
  source: string;
}

export interface DescribedCustom {
  properties: Record<string, unknown>;
}

export function customLink(
  name: string,
  described: DescribedCustom,
): CustomLink {
  if (!name) {
    throw new Error(
      "a link is published under no name; the name is what a transform reads it by",
    );
  }
  const properties = described.properties;
  if (Object.keys(properties).length === 0) {
    throw new Error(
      `custom link ${name} carries no properties; a custom link is the values a transform reads out of it, so an empty one has nothing to read`,
    );
  }
  for (const [key, value] of Object.entries(properties)) {
    if (value === undefined) {
      throw new Error(
        `custom link ${name} carries ${key} as undefined; a property a transform names must have a value the deploy can insert`,
      );
    }
  }
  return { name, custom: properties, source };
}
