import { custom, postgres } from "./resource.js";

export type { CustomBinding } from "./custom.js";
export type { Grant, SSTInclude } from "./grants.js";
export type { PostgresBinding, PostgresProperties } from "./postgres.js";
export type {
  BindOptions,
  CustomProperties,
  DescribedPostgresResource,
  SSTPostgresBindable,
} from "./resource.js";

/**
 * The ocel bindings this SST app publishes, one call per binding.
 *
 * A binding is one resource an app reaches, published under a name of this
 * program's choosing — `bind.postgres("sst-pg-orders", …)` here, and the app's
 * config binds a declared resource to it with
 * `"bindings": { "postgres": { "orders": "sst-pg-orders" } }`. Each
 * call is one Pulumi resource, so removing the call removes the published
 * record, and there is one function per ocel binding type an app resolves: a
 * resource ocel cannot type is not bindable by name, and nothing here refuses
 * at apply what it could refuse by having no function to call.
 *
 * `custom` is the exception an app never sees: it publishes values ocel neither
 * types nor delivers, read only by a transform module through
 * `bindings.<name>.<property>`.
 */
export const bind = { postgres, custom };
