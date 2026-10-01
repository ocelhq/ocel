/**
 * Thrown when a value written to an entry, or read from one, is not a value of the entry's
 * shape: a `json` value that fails its schema, or a `counter` that holds no integer.
 */
export class InvalidKVValueError extends Error {
  override name = "InvalidKVValueError";

  constructor(
    /** The key whose value is invalid. */
    readonly key: string,
    reason: string,
  ) {
    super(`key "${key}" ${reason}`);
  }
}
