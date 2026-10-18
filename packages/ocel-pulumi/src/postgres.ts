import { source } from "./cli.js";
import { type Grant, scoped } from "./grants.js";
import type { PublishedBinding } from "./published.js";

/** The typed properties a postgres binding contains, as `common.bindings.v1.PostgresProperties`. */
export interface PostgresProperties {
  host: string;
  port: number;
  database: string;
  username: string;
  password: string;
}

/** One `common.bindings.v1.Binding` containing postgres properties, ready for protobuf JSON. */
export interface PostgresBinding {
  name: string;
  postgres: PostgresProperties;
  source: string;
}

export interface DescribedPostgres {
  properties: Record<string, unknown>;
  grants?: Grant[];
}

export function publishedPostgres(
  name: string,
  described: DescribedPostgres,
): PublishedBinding<PostgresBinding> {
  if (!name) {
    throw new Error(
      "a binding is published under no name; the name is what a consuming app binds to",
    );
  }
  const published: PublishedBinding<PostgresBinding> = {
    binding: { name, postgres: propertiesFor(name, described.properties), source },
  };
  const grants = scoped(name, described.grants);
  if (grants) {
    published.grants = grants;
  }
  return published;
}

const textFields = ["host", "database", "username", "password"] as const;

function propertiesFor(name: string, properties: Record<string, unknown>): PostgresProperties {
  const out = {} as PostgresProperties;
  for (const field of textFields) {
    const value = properties[field];
    if (typeof value !== "string" || value === "") {
      throw new Error(
        `postgres binding ${name} has no ${field}; a postgres binding is its host, port, database, username and password, and an app resolving it reads every one`,
      );
    }
    out[field] = value;
  }
  out.port = portFor(name, properties.port);
  return out;
}

function portFor(name: string, value: unknown): number {
  const port = typeof value === "string" ? Number(value) : value;
  if (typeof port !== "number" || !Number.isInteger(port) || port <= 0) {
    throw new Error(
      `postgres binding ${name} has port ${JSON.stringify(value ?? null)}, and a port is a whole number an app can connect to`,
    );
  }
  return port;
}
