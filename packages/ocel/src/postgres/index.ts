import { Pool } from "pg";
import { ResourceType } from "../gen/proto/app/resources/v1/resources_pb.js";
import { getConfig } from "../utils/get-config.js";
import { unprovisionedPhase, unprovisionedProxy } from "../utils/phase.js";
import { reference } from "../utils/reference.js";
import { Postgres, type PostgresConfig } from "./pg.js";

export { UnprovisionedResourceError } from "../utils/phase.js";

type PgReturn = Pool & { connectionString: string };

/**
 * Declares a postgres database named `id` and returns the pool an app reads it through.
 * Call it from a file under the project's discovery folder: during discovery the call is
 * the declaration, and at runtime it reads the binding the deploy delivered for that name.
 */
export function postgres(id: string, config?: PostgresConfig): PgReturn {
  return pool(new Postgres(id, config).__id());
}

/**
 * References the postgres database named `id`, declared once elsewhere in the project in
 * any language, and returns the same pool {@link postgres} does. It never declares. Call
 * it from a file under the project's discovery folder and import that file from the app:
 * during discovery the call records that the file uses the database, so the deploy grants
 * it to every app that imports the file.
 */
postgres.ref = (id: string): PgReturn => {
  reference(ResourceType.POSTGRES, id);
  return pool(id);
};

function pool(id: string): PgReturn {
  if (unprovisionedPhase()) {
    return unprovisionedProxy<PgReturn>(`postgres("${id}")`);
  }

  const { host, port, database, username, password } = getConfig(id, "postgres");

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
