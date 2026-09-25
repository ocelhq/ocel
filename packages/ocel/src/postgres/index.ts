import { Pool, type PoolConfig } from "pg";
import {
  type PostgresProperties,
  PostgresTlsMode,
} from "../gen/proto/common/bindings/v1/bindings_pb.js";
import { unprovisionedPhase, unprovisionedProxy } from "../utils/phase.js";
import { Postgres, type PostgresConfig } from "./pg.js";

export { UnprovisionedResourceError } from "../utils/phase.js";

type PgReturn = Pool & { connectionString: string };

/**
 * Declares a postgres database named `id` and returns a `pg` pool connected to it.
 *
 * At deploy the database is provisioned in your account, or bound to the record
 * `bindings` names; the pool connects through the record's url when it carries
 * one, reading its `sslmode` as libpq does, and otherwise through its host, port,
 * database, username and password, encrypted as its tls mode asks.
 * `connectionString` is the same connection as a URL, for tools that take one.
 */
export function postgres(id: string, config?: PostgresConfig): PgReturn {
  const pg = new Postgres(id, config);

  if (unprovisionedPhase()) {
    return unprovisionedProxy<PgReturn>(`postgres("${id}")`);
  }

  const properties = pg.__config();
  const client = new Pool(poolConfig(properties));

  return Object.assign(client, { connectionString: connectionStringOf(properties) });
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
