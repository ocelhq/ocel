import { Postgres, type PostgresConfig } from "./pg";
import { Pool } from "pg";

export function postgres(id: string, config?: PostgresConfig): Pool {
  const pg = new Postgres(id, config);
  const { connectionString } = pg.__config();

  const client = new Pool({
    connectionString,
  });

  return client;
}
