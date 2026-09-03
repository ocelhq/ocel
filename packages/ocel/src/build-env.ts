import type { StandardSchemaV1 } from "@standard-schema/spec";
import { isUsableKey } from "./env/definition.js";
import { parse } from "./env/standard.js";

export class BuildEnvError extends Error {
  override name = "BuildEnvError";
}

export type BuildEnv<TSchemas extends Record<string, StandardSchemaV1>> = {
  readonly [K in keyof TSchemas]: StandardSchemaV1.InferOutput<TSchemas[K]>;
};

/**
 * Declares the environment the config itself needs while it is being evaluated
 * — values read from the shell or the project's `.env` — and validates them
 * before they reach provider options, so a missing or malformed value fails at
 * the config rather than mid-deploy.
 *
 * Anything returned here can end up in deployment records. A real secret
 * belongs in the runtime `secret` env class instead.
 */
export function buildEnv<
  const TSchemas extends Record<string, StandardSchemaV1>,
>(schemas: TSchemas): BuildEnv<TSchemas> {
  for (const key of Object.keys(schemas)) {
    if (!isUsableKey(key)) {
      throw new BuildEnvError(
        `'${key}' is not a usable variable name: use upper-case letters, digits and underscores, starting with a letter or underscore.`,
      );
    }
  }

  const resolved = new Map<string, unknown>();
  return new Proxy({} as BuildEnv<TSchemas>, {
    get(_target, property) {
      if (typeof property === "symbol") return undefined;

      const key = property;
      const schema = schemas[key];
      if (!schema) {
        throw new BuildEnvError(
          `'${key}' is not declared in the buildEnv call. Add it there before reading it.`,
        );
      }
      if (resolved.has(key)) return resolved.get(key);

      const result = parse(schema, process.env[key]);
      if (!result.ok) {
        throw new BuildEnvError(
          `'${key}' ${result.message} — set it in your shell or in the project's .env`,
        );
      }
      resolved.set(key, result.value);
      return result.value;
    },
  });
}
