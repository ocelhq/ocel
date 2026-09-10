import { source } from "./cli.js";
import { type Grant, grantsFor, type SSTInclude, scoped } from "./grants.js";

/** The typed properties a postgres binding carries, as `common.bindings.v1.PostgresProperties`. */
export interface PostgresProperties {
  host: string;
  port: number;
  database: string;
  username: string;
  password: string;
}

/** One `common.bindings.v1.Binding` holding postgres properties, ready for protobuf JSON. */
export interface PostgresBinding {
  name: string;
  postgres: PostgresProperties;
  grants?: Grant[];
  source: string;
}

export interface DescribedPostgres {
  properties: Record<string, unknown>;
  include?: SSTInclude[];
  grants?: Grant[];
}

export function postgresBinding(name: string, described: DescribedPostgres): PostgresBinding {
  if (!name) {
    throw new Error(
      "a binding is published under no name; the name is what a consuming app binds to",
    );
  }
  const binding: PostgresBinding = {
    name,
    postgres: propertiesFor(name, described.properties),
    source,
  };
  const grants = scoped(name, described.grants ?? grantsFor(name, described.include));
  if (grants) {
    binding.grants = grants;
  }
  return binding;
}

const textFields = ["host", "database", "username", "password"] as const;

function propertiesFor(name: string, properties: Record<string, unknown>): PostgresProperties {
  const out = {} as PostgresProperties;
  for (const field of textFields) {
    const value = properties[field];
    if (typeof value !== "string" || value === "") {
      throw new Error(
        `postgres binding ${name} carries no ${field}; a postgres binding is its host, port, database, username and password, and an app resolving it reads every one`,
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
      `postgres binding ${name} carries port ${JSON.stringify(value ?? null)}, and a port is a whole number an app can connect to`,
    );
  }
  return port;
}
