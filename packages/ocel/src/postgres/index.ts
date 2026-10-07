import { Pool, type PoolConfig } from "pg";
import { unprovisioned, unprovisionedPhase } from "../binding/unprovisioned.js";
import {
  type PostgresProperties,
  PostgresTlsMode,
} from "../gen/proto/common/bindings/v1/bindings_pb.js";
import { Postgres, type PostgresConfig } from "./pg.js";

export { UnprovisionedResourceError } from "../binding/unprovisioned.js";

type PgReturn = Pool & { connectionString: string };

/**
 * Declares a postgres database named `id` and returns a `pg` pool connected to it.
 *
 * At deploy the database is provisioned in your account, or bound to the record
 * `bindings` names; the pool connects through the record's url when it has
 * one, reading its `sslmode` as libpq does, and otherwise through its host, port,
 * database, username and password, encrypted as its tls mode asks.
 * `connectionString` is the same connection as a URL, for tools that take one.
 */
export function postgres(id: string, config?: PostgresConfig): PgReturn {
  const pg = new Postgres(id, config);

  let pool: PgReturn | undefined;
  const openPool = (access: string): PgReturn => {
    if (unprovisionedPhase()) {
      throw unprovisioned(`postgres("${id}")`, access);
    }
    if (!pool) {
      const properties = pg.__config();
      pool = Object.assign(new Pool(poolConfig(properties)), {
        connectionString: connectionStringOf(properties),
      });
    }
    return pool;
  };

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
  if (properties.url) {
    return { connectionString: withLibpqSslmodes(properties.url) };
  }
  const { host, port, database, username, password } = properties;
  const config: PoolConfig = { host, port, database, user: username, password };
  switch (properties.tlsMode) {
    case PostgresTlsMode.REQUIRE:
      config.ssl = { rejectUnauthorized: false };
      break;
    case PostgresTlsMode.VERIFY_FULL:
      config.ssl = properties.tlsCa
        ? { rejectUnauthorized: true, ca: properties.tlsCa }
        : { rejectUnauthorized: true };
      break;
  }
  return config;
}

function withLibpqSslmodes(url: string): string {
  if (/[?&]uselibpqcompat=/.test(url)) {
    return url;
  }
  return `${url}${url.includes("?") ? "&" : "?"}uselibpqcompat=true`;
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
