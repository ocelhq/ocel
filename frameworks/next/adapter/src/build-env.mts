const programmableEdgeKind = "cloudflare";

export function isProgrammableEdge(kind: string | undefined): boolean {
  return kind === programmableEdgeKind;
}

function splitCommaSeparated(declared: string | undefined): Set<string> {
  return new Set(
    (declared ?? "")
      .split(",")
      .map((name) => name.trim())
      .filter(Boolean),
  );
}

export function readWaivedNeeds(): Set<string> {
  return splitCommaSeparated(process.env.OCEL_ALLOW_DEGRADED);
}

export function readPublicKeys(): Set<string> {
  return splitCommaSeparated(process.env.OCEL_PUBLIC_KEYS);
}

export function readMaxFunctionBytes(): number | undefined {
  const declared = process.env.OCEL_MAX_FUNCTION_BYTES;
  if (!declared) return undefined;
  const bytes = Number(declared);
  if (!Number.isSafeInteger(bytes) || bytes <= 0) {
    throw new Error(
      `ocel: OCEL_MAX_FUNCTION_BYTES is "${declared}", and a function's size budget is a positive whole number of bytes`,
    );
  }
  return bytes;
}
