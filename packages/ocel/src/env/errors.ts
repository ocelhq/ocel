/** Thrown when a `defineEnv` call declares something ocel refuses, such as a reserved key or a public confidential variable. */
export class EnvDefinitionError extends Error {
  override name = "EnvDefinitionError";
}

/** Thrown when a declared variable cannot be read: its value is missing or fails its schema, or it is read during discovery. */
export class EnvValueError extends Error {
  override name = "EnvValueError";
}

/** Thrown when edge code reads a variable the edge cannot deliver, such as a `secret`. */
export class EnvEdgeError extends Error {
  override name = "EnvEdgeError";
}

/** Thrown when browser code reads a server-only variable, or a bundle was built without the public values inlined. */
export class EnvClientError extends Error {
  override name = "EnvClientError";
}
