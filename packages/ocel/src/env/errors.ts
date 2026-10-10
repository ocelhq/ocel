export class EnvDefinitionError extends Error {
  override name = "EnvDefinitionError";
}

export class EnvValueError extends Error {
  override name = "EnvValueError";
}

export class EnvEdgeError extends Error {
  override name = "EnvEdgeError";
}

export class EnvClientError extends Error {
  override name = "EnvClientError";
}
