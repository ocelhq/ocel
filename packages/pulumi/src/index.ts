import { custom, postgres } from "./resource.js";

export type { CustomLink } from "./custom.js";
export type { Grant } from "./grants.js";
export type { PostgresLink, PostgresProperties } from "./postgres.js";
export type {
  DescribedCustomResource,
  DescribedPostgresResource,
  LinkOptions,
} from "./resource.js";

/**
 * The ocel links this Pulumi program publishes, one call per link.
 *
 * A link is one resource an app reaches, published under the name the app binds
 * to — `postgres("orders")` in the app, `link.postgres("orders", …)` here. Each
 * call is one Pulumi resource, so removing the call removes the published
 * record, and there is one function per ocel link type an app resolves: a
 * resource ocel cannot type is not linkable by name, and nothing here refuses
 * at update what it could refuse by having no function to call.
 *
 * `custom` is the exception an app never sees: it publishes values ocel neither
 * types nor delivers, read only by a transform module through
 * `links.<name>.<property>`.
 */
export const link = { postgres, custom };
