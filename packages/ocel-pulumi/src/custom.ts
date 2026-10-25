import { source } from "./cli.js";
import { type Grant, scoped } from "./grants.js";
import type { PublishedBinding } from "./published.js";

/** One `common.bindings.v1.Binding` containing a custom record's open properties, ready for protobuf JSON. */
export interface CustomBinding {
  name: string;
  custom: Record<string, unknown>;
  source: string;
}

export function publishedCustom(
  name: string,
  properties: Record<string, unknown>,
  grants?: Grant[],
): PublishedBinding<CustomBinding> {
  if (!name) {
    throw new Error(
      "a binding is published under no name; the name is what a transform reads it by",
    );
  }
  const granted = scoped(name, grants);
  if (Object.keys(properties).length === 0 && !granted) {
    throw new Error(
      `custom binding ${name} has no properties and no grants; a custom binding is the values a transform reads out of it and the grants the apps bound to it receive, so an empty one has nothing to give`,
    );
  }
  for (const [key, value] of Object.entries(properties)) {
    if (value === undefined) {
      throw new Error(
        `custom binding ${name} has ${key} as undefined; a property a transform names must have a value the deploy can insert`,
      );
    }
  }
  const published: PublishedBinding<CustomBinding> = {
    binding: { name, custom: properties, source },
  };
  if (granted) {
    published.grants = granted;
  }
  return published;
}
