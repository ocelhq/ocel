import { z } from "zod";
import { readLive } from "../env/live.js";
import { shapeOf } from "./registry.js";

/**
 * Thrown when a link cannot be read: nothing delivered it, what arrived is not
 * a link record, its token is not the one asked for, or it is missing a
 * property its type requires.
 */
export class LinkError extends Error {
  override name = "LinkError";
}

// TODO(#304, #305): this key and the {type, properties} envelope are the
// consuming half of a contract whose producer does not exist yet — nothing in
// the repo writes OCEL_LINK_*. Reconcile both against the per-link vars
// partition and the cold-start delivery when they land; a divergence here is a
// runtime failure, not a compile one.
const LINK_PREFIX = "OCEL_LINK_";

const recordSchema = z.object({
  type: z.string().min(1),
  properties: z.record(z.string(), z.string()),
});

export function resolve(
  name: string,
  type: string,
): Record<string, string> {
  if (process.env.OCEL_PHASE === "discovery") {
    return unresolved(name);
  }

  const key = LINK_PREFIX + name;
  const raw = readLive(key) ?? process.env[key];
  if (raw === undefined) {
    throw new LinkError(
      `Link '${name}' was not delivered to this app. Nothing published a record under that name for this environment.`,
    );
  }

  const record = recordSchema.safeParse(parse(name, raw));
  if (!record.success) {
    throw new LinkError(
      `Link '${name}' did not deliver a link record: ${z.prettifyError(record.error)}`,
    );
  }

  if (record.data.type !== type) {
    throw new LinkError(
      `Link '${name}' was read as '${type}' but carries '${record.data.type}'.`,
    );
  }

  const shape = shapeOf(type);
  if (!shape) return record.data.properties;

  const properties = shape.safeParse(record.data.properties);
  if (!properties.success) {
    throw new LinkError(
      `Link '${name}' does not carry the properties '${type}' requires: ${z.prettifyError(properties.error)}`,
    );
  }

  return properties.data;
}

function parse(name: string, raw: string): unknown {
  try {
    return JSON.parse(raw);
  } catch {
    throw new LinkError(`Link '${name}' did not deliver parseable JSON.`);
  }
}

function unresolved(name: string): Record<string, string> {
  return new Proxy({} as Record<string, string>, {
    get(_target, property) {
      if (typeof property === "symbol") return undefined;
      throw new LinkError(
        `Link '${name}' cannot be read during discovery: tried to access '${String(property)}' before its values were resolved.`,
      );
    },
  });
}
