// These errors live apart from the read path itself because the read path has
// two builds — the node one and the edge one — and a definitions module shared
// by both tiers must be able to name the same error either way. An error only
// one module can raise stays with that module.

// EnvValueError is a variable that cannot be read: nothing set it, or what is
// set does not satisfy its schema. It names the key and the command that
// fixes it, because that is the whole remedy.
export class EnvValueError extends Error {
  override name = "EnvValueError";
}

// EnvEdgeError is a variable read from an edge entry. It is a class of its own
// rather than an EnvValueError because nothing about the value is wrong: no
// variable class is deliverable to the edge tier at all, so the remedy is
// about the entry, never about the value.
export class EnvEdgeError extends Error {
  override name = "EnvEdgeError";
}
