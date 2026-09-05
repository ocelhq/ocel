import { unprovisionedPhase, unprovisionedProxy } from "../utils/phase.js";
import { Postgres, type PostgresConfig } from "./pg.js";
import { Pool } from "pg";

export { UnprovisionedResourceError } from "../utils/phase.js";

type PgReturn = Pool & { connectionString: string };

export function postgres(id: string, config?: PostgresConfig): PgReturn {
  const pg = new Postgres(id, config);

  if (unprovisionedPhase()) {
    return unprovisionedProxy<PgReturn>(`postgres("${id}")`);
  }

  const { host, port, database, username, password } = pg.__config();

  const client = new Pool({ host, port, database, user: username, password });

  return Object.assign(client, {
    connectionString: connectionStringFor(host, port, database, username, password),
  });
}

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
