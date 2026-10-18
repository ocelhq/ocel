/** Thrown when an operation that cannot answer with `undefined` names an object the bucket does not have. */
export class ObjectNotFoundError extends Error {
  /** The key that named nothing. */
  readonly key: string;

  constructor(key: string, options?: ErrorOptions) {
    super(`the bucket has no object under "${key}"`, options);
    this.name = "ObjectNotFoundError";
    this.key = key;
  }
}

/** Thrown when a write set `ifNoneMatch` or `ifMatch` and the object did not meet it. */
export class PreconditionFailedError extends Error {
  /** The key whose current state refused the write. */
  readonly key: string;

  constructor(key: string, options?: ErrorOptions) {
    super(`the object under "${key}" did not meet the condition this write set`, options);
    this.name = "PreconditionFailedError";
    this.key = key;
  }
}
