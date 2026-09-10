import type { Linked } from "./output";

/**
 * A deep-partial of one underlying resource's args, with every leaf open to a
 * binding output as well as a written-down value.
 */
export type Patch<T> = T extends undefined | null
  ? never
  : T extends readonly (infer E)[]
    ? Linked<Patch<E>[]>
    : T extends object
      ? { readonly [K in keyof T]?: Patch<T[K]> }
      : Linked<T>;
