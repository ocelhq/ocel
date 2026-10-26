/** A value as an SST app hands it over: plain, a promise of it, or an output that resolves before it is published. */
export type Input<T> = T | Promise<T> | { apply(f: (value: T) => unknown): unknown };
