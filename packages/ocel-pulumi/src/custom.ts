import { source } from "./cli.js";

/** One `common.bindings.v1.Binding` holding a custom record's open properties, ready for protobuf JSON. */
export interface CustomBinding {
  name: string;
  custom: Record<string, unknown>;
  source: string;
}

export function customBinding(name: string, properties: Record<string, unknown>): CustomBinding {
  if (!name) {
    throw new Error(
      "a binding is published under no name; the name is what a transform reads it by",
    );
  }
  if (Object.keys(properties).length === 0) {
    throw new Error(
      `custom binding ${name} carries no properties; a custom binding is the values a transform reads out of it, so an empty one has nothing to read`,
    );
  }
  for (const [key, value] of Object.entries(properties)) {
    if (value === undefined) {
      throw new Error(
        `custom binding ${name} carries ${key} as undefined; a property a transform names must have a value the deploy can insert`,
      );
    }
  }
  return { name, custom: properties, source };
}
