export type {
  Gate,
  Patch,
  Transform,
  TransformFn,
  TransformInputs,
  TransformRule,
  TransformRules,
} from "./define";
export { defineTransform } from "./define";

import { type LinkPlaceholdersOf, links as openLinks } from "./output";

/**
 * The records published to the coordinate a deploy targets, each property
 * under the type it carries. `ocel link generate` writes an augmentation of
 * this interface from the records themselves; until something does, every
 * name is open and the deploy is what checks it.
 */
export interface Links {}

/**
 * Whether `ocel link generate` has written the records down. It augments this
 * separately from `Links`, so a coordinate that published nothing still closes
 * `Links` to the empty set instead of reading as never generated.
 */
export interface LinksGenerated {}

/** The placeholders a transform module reads, narrowed by whatever was generated. */
export type TransformLinks = LinkPlaceholdersOf<Links, LinksGenerated>;

/**
 * The published records a transform module reads, one placeholder per property
 * named. Nothing is resolved here: `links.orders.host` is the instruction the
 * deploy carries out against the records published to the environment it targets.
 */
export const links = openLinks as TransformLinks;

export type {
  Linked,
  LinkOutput,
  LinkOutputRef,
  LinkPlaceholders,
  LinkPlaceholdersOf,
  LinkProperties,
} from "./output";
export { isLinkOutput } from "./output";
export type {
  AwsSurfaces,
  BucketBucketSurface,
  BucketCorsSurface,
  BucketNotificationSurface,
  BucketUploadCompleterSurface,
  EnvClass,
  FunctionLambdaSurface,
  FunctionUrlSurface,
  FunctionVpcSurface,
  GateContext,
  PostgresClusterSurface,
  PostgresInstanceSurface,
  SurfaceType,
  TagMap,
  TransformContext,
} from "./surface";
