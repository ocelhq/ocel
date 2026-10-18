import { Pool, type PoolConfig } from "pg";
import { lazyBinding } from "../binding/binding.js";
import { declare } from "../declaration/declare.js";
import {
  type PostgresProperties,
  PostgresTlsMode,
} from "../gen/proto/common/bindings/v1/bindings_pb.js";
import { PostgresSocket, tlsPolicyOf, withoutTlsParams } from "./tls.js";

export { MissingBindingError } from "../binding/errors.js";
export { UnprovisionedResourceError } from "../binding/unprovisioned.js";

type PgReturn = Pool & { connectionString: string };

/** How a postgres database is declared. */
export interface PostgresOptions {
  /** The major postgres version to provision; without it, `17`. */
  version?: string;
}

/**
 * Declares a postgres database named `id` and returns a `pg` pool connected to it.
 *
 * At deploy the database is provisioned in your account, or bound to the record
 * `bindings` names; the pool connects through the record's url when it has
 * one, and otherwise through its host, port, database, username and password.
 * TLS follows libpq: the url's `sslmode`, or else the record's tls mode, and
 * `prefer` when neither names one, so the connection is encrypted whenever the
 * server accepts TLS. A record's CA is trusted and its tls server name verified,
 * url or not, as a record pointing at a port forward needs.
 * `connectionString` is the same connection as a URL, for tools that take one.
 */
export function postgres(id: string, options: PostgresOptions = {}): PgReturn {
  declare(id, { case: "postgres", value: { version: options.version ?? "17" } });

  const openPool = lazyBinding(
    `postgres("${id}")`,
    id,
    "postgres",
    (properties): PgReturn =>
      Object.assign(new Pool(poolConfig(properties)), {
        connectionString: connectionStringOf(properties),
      }),
  );

  const bound = new Map<PropertyKey, { method: unknown; bound: unknown }>();
  const proxy: PgReturn = new Proxy(Object.create(Pool.prototype) as PgReturn, {
    get(_target, prop) {
      if (prop === "then") {
        return undefined;
      }
      const opened = openPool(String(prop));
      const value = Reflect.get(opened, prop, opened);
      if (typeof value !== "function") {
        return value;
      }
      const cached = bound.get(prop);
      if (cached && cached.method === value) {
        return cached.bound;
      }
      const method = (...args: unknown[]) => {
        const result = value.apply(opened, args);
        return result === opened ? proxy : result;
      };
      bound.set(prop, { method: value, bound: method });
      return method;
    },
    set(_target, prop, value) {
      const opened = openPool(String(prop));
      return Reflect.set(opened, prop, value, opened);
    },
    has(_target, prop) {
      return Reflect.has(openPool(String(prop)), prop);
    },
  });
  return proxy;
}

function poolConfig(properties: PostgresProperties): PoolConfig {
  const policy = tlsPolicyOf(properties);
  const negotiated = { ssl: false, stream: () => new PostgresSocket(policy) };
  if (properties.url) {
    return { connectionString: withoutTlsParams(properties.url), ...negotiated };
  }
  const { host, port, database, username, password } = properties;
  return { host, port, database, user: username, password, ...negotiated };
}

const sslmodes: Record<PostgresTlsMode, string | undefined> = {
  [PostgresTlsMode.UNSPECIFIED]: undefined,
  [PostgresTlsMode.REQUIRE]: "require",
  [PostgresTlsMode.VERIFY_FULL]: "verify-full",
};

function connectionStringOf(properties: PostgresProperties): string {
  if (properties.url) {
    return properties.url;
  }
  const { host, port, database, username, password } = properties;
  const url = new URL(connectionStringFor(host, port, database, username, password));
  const sslmode = sslmodes[properties.tlsMode];
  if (sslmode) {
    url.searchParams.set("sslmode", sslmode);
  }
  return url.toString();
}

/**
 * Renders a postgres connection URL from its parts, percent-encoding the
 * username and password so any character in either survives.
 */
export function connectionStringFor(
  host: string,
  port: number,
  database: string,
  username: string,
  password: string,
): string {
  const url = new URL(`postgres://${host}:${port}/`);
  url.pathname = `/${database}`;
  url.username = username;
  url.password = password;
  return url.toString();
}
