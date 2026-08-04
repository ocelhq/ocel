// The names the build writes and the runtime reads back, which the two must
// spell identically: what a route's cache entry is called, and what the build's
// per-variant header projection is called. A drift in either makes every runtime
// lookup miss and quietly disables PPR at the edge, so both are derived here and
// imported by every side rather than spelled per package.
//
// This module is deliberately a leaf: it imports nothing, not even from its own
// package, and it uses no TypeScript syntax Node cannot erase. The Next adapter
// reaches it from `packages/next-runtime`, whose built dist is executed by plain
// Node — which strips the types out of a `.mts` file but does not rewrite the
// `.mjs` specifiers TypeScript emits for relative imports, and rejects outright
// the constructs that have no erasure (enum, namespace, parameter properties).
// index.mts is unloadable there for the first reason; one import or one `enum`
// added here would make this module unloadable too.
//
// The invariant is enforced from the consumer that needs it, by
// packages/next-runtime/test/plain-node-imports.test.mts, which loads every
// specifier the adapter imports the way the adapter itself resolves it.

// cacheKey is the name a route's entry is stored under: the build seeds it and
// the cache handler looks it up. Route entries only — fetch entries are keyed by
// their own hash into a separate, AWS-private bucket, which no edge reader can
// reach, so their keying lives with the Lambda store rather than here.
export function cacheKey(key: string): string {
  return key === "/" || key === "" ? "index" : key.replace(/^\//, "");
}

// The build's projection of each prerendered route's per-variant headers, laid
// beside the function by the adapter under this name and read back from the root
// of the function's own task directory. It ships in the bundle rather than being
// fetched, so the code reading it and the build that wrote it are the same
// artifact and can never be a version apart.
export const variantHeadersFile = "variant-headers.json";
