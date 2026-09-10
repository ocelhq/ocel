export type {
  EnvClass,
  Gate,
  GateContext,
  ProviderName,
  ProviderSurfaces,
  TagMap,
  TransformDefinition,
  TransformInputs,
  TransformRule,
  TransformRules,
} from "./define";
export { defineTransform } from "./define";
export type { Patch } from "./patch";

import { type BindingPlaceholdersOf, bindings as openBindings } from "./output";

/**
 * The records this project's config binds, keyed by resource type and then by
 * the name the config keys them under, with `custom` holding the records
 * nothing declared. `ocel bindings generate` writes an augmentation of this
 * interface from the records themselves; until something does, every name is
 * open and the deploy is what checks it.
 */
export interface Bindings {}

/**
 * Whether `ocel bindings generate` has written the records down. It augments this
 * separately from `Bindings`, so a coordinate that published nothing still closes
 * `Bindings` to the empty set instead of reading as never generated.
 */
export interface BindingsGenerated {}

/** The placeholders a transform module reads, narrowed by whatever was generated. */
export type TransformBindings = BindingPlaceholdersOf<Bindings, BindingsGenerated>;

/**
 * The records a transform module reads, one placeholder per property named.
 * Nothing is resolved here: `bindings.custom.network.subnetIds` is the
 * instruction the deploy carries out against the records published to the
 * environment it targets.
 */
export const bindings = openBindings as TransformBindings;

export type {
  BindingNames,
  BindingOutput,
  BindingOutputRef,
  BindingPlaceholders,
  BindingPlaceholdersOf,
  BindingProperties,
  Linked,
} from "./output";
export { isBindingOutput } from "./output";
