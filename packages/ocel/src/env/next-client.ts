import { EnvClientError } from "./client.js";
import {
  isPublic,
  type NextDefinitions,
  type NextEnv,
  type PublicPlain,
  refusePublicConfidential,
} from "./next-shared.js";
import { parse } from "./standard.js";

// The adapter's compiler.define replaces this exact expression with a string literal.
function publicValues(): Record<string, string> {
  const raw = process.env.OCEL_PUBLIC_ENV;
  if (typeof raw !== "string") {
    throw new EnvClientError(
      "no public variables were inlined into this bundle: the Next build ran without the ocel adapter, which defines process.env.OCEL_PUBLIC_ENV.",
    );
  }
  return JSON.parse(raw) as Record<string, string>;
}

export function defineEnv<const T extends NextDefinitions>(
  definitions: T & PublicPlain<T>,
): NextEnv<T> {
  refusePublicConfidential(definitions);
  const resolved = new Map<string, unknown>();

  return new Proxy({} as NextEnv<T>, {
    get(_target, key) {
      if (typeof key === "symbol") return undefined;
      const definition = (definitions as NextDefinitions)[key];
      if (!definition) {
        throw new EnvClientError(
          `'${key}' is not a declared variable. Add it to a defineEnv call.`,
        );
      }
      if (!isPublic(key)) {
        throw new EnvClientError(
          `'${key}' is server-only and cannot be read in the browser. Only NEXT_PUBLIC_ variables reach client code; read '${key}' in a server component, route handler or server action.`,
        );
      }
      if (resolved.has(key)) return resolved.get(key);
      const raw = publicValues()[key];
      if (raw === undefined && !definition.schema) {
        throw new EnvClientError(`'${key}' had no value when the app was built.`);
      }
      let value: unknown = raw;
      if (definition.schema) {
        const result = parse(definition.schema, raw);
        if (!result.ok) {
          throw new EnvClientError(
            `'${key}' does not satisfy its schema: ${result.message}. Fix it with \`ocel env set ${key}=<VALUE>\` and rebuild.`,
          );
        }
        value = result.value;
      }
      resolved.set(key, value);
      return value;
    },
  });
}
