// stableStringify serializes with keys in sorted order at every level, so a
// value that has not changed always produces the same bytes — what both the
// edge bundle's content hash and the image config's hash rest on.
export function stableStringify(value: unknown): string {
  return JSON.stringify(value, (_key, val) =>
    val && typeof val === "object" && !Array.isArray(val)
      ? Object.fromEntries(
          Object.entries(val).sort(([a], [b]) => (a < b ? -1 : 1)),
        )
      : val,
  );
}
