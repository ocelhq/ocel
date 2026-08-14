import { z } from "zod";
import { readLive } from "../env/live.js";
import { shapeOf } from "./registry.js";

export class LinkError extends Error {
  override name = "LinkError";
}

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
      `Link '${name}' has no value. Add it to the app's 'links' binding in ocel.config.ts and publish it before deploying.`,
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
      throw new LinkError(
        `Link '${name}' cannot be read during discovery: tried to access '${String(property)}' before its values were resolved.`,
      );
    },
  });
}
