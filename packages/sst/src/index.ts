import { postgres } from "./resource.js";

export type { Grant, SSTInclude } from "./grants.js";
export type { PostgresLink, PostgresProperties } from "./postgres.js";
export type {
  DescribedPostgresResource,
  LinkOptions,
  SSTPostgresLinkable,
} from "./resource.js";

/**
 * The ocel links this SST app publishes, one call per link.
 *
 * A link is one resource an app reaches, published under the name the app binds
 * to — `postgres("orders")` in the app, `link.postgres("orders", …)` here. Each
 * call is one Pulumi resource, so removing the call removes the published
 * record, and there is one function per ocel link type: a resource ocel cannot
 * type is not linkable, and nothing here refuses at apply what it could refuse
 * by having no function to call.
 */
export const link = { postgres };
