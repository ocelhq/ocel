import { source } from "./cli.js";
import { grantsFor, scoped, type Grant, type SSTInclude } from "./grants.js";

/** The typed properties a postgres link carries, as `common.links.v1.PostgresProperties`. */
export interface PostgresProperties {
  host: string;
  port: number;
  database: string;
  username: string;
  password: string;
}

/** One `common.links.v1.Link` holding postgres properties, ready for protobuf JSON. */
export interface PostgresLink {
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

export function postgresLink(
  name: string,
  described: DescribedPostgres,
): PostgresLink {
  if (!name) {
    throw new Error(
      "a link is published under no name; the name is what a consuming app binds to",
    );
  }
  const link: PostgresLink = {
    name,
    postgres: propertiesFor(name, described.properties),
    source,
  };
  const grants = scoped(
    name,
    described.grants ?? grantsFor(name, described.include),
  );
  if (grants) {
    link.grants = grants;
  }
  return link;
}

const textFields = ["host", "database", "username", "password"] as const;

function propertiesFor(
  name: string,
  properties: Record<string, unknown>,
): PostgresProperties {
  const out = {} as PostgresProperties;
  for (const field of textFields) {
    const value = properties[field];
    if (typeof value !== "string" || value === "") {
      throw new Error(
        `postgres link ${name} carries no ${field}; a postgres link is its host, port, database, username and password, and an app resolving it reads every one`,
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
      `postgres link ${name} carries port ${JSON.stringify(value ?? null)}, and a port is a whole number an app can connect to`,
    );
  }
  return port;
}
