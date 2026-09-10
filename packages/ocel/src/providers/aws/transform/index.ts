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

import { type BindingPlaceholdersOf, bindings as openBindings } from "./output";

/**
 * The records published to the coordinate a deploy targets, each property
 * under the type it carries. `ocel binding generate` writes an augmentation of
 * this interface from the records themselves; until something does, every
 * name is open and the deploy is what checks it.
 */
export interface Bindings {}

/**
 * Whether `ocel binding generate` has written the records down. It augments this
 * separately from `Bindings`, so a coordinate that published nothing still closes
 * `Bindings` to the empty set instead of reading as never generated.
 */
export interface BindingsGenerated {}

/** The placeholders a transform module reads, narrowed by whatever was generated. */
export type TransformBindings = BindingPlaceholdersOf<Bindings, BindingsGenerated>;

/**
 * The published records a transform module reads, one placeholder per property
 * named. Nothing is resolved here: `bindings.orders.host` is the instruction the
 * deploy carries out against the records published to the environment it targets.
 */
export const bindings = openBindings as TransformBindings;

export type {
  BindingOutput,
  BindingOutputRef,
  BindingPlaceholders,
  BindingPlaceholdersOf,
  BindingProperties,
  Bound,
} from "./output";
export { isBindingOutput } from "./output";
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
