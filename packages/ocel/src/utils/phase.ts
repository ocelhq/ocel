/** A phase in which a declared resource exists as a declaration alone. */
export type UnprovisionedPhase = "discovery" | "resources-suppressed";

/**
 * The phase this process runs in when nothing it declares was provisioned,
 * and `undefined` when a resource is there to be read.
 */
export function unprovisionedPhase(): UnprovisionedPhase | undefined {
  const phase = process.env.OCEL_PHASE;
  return phase === "discovery" || phase === "resources-suppressed"
    ? phase
    : undefined;
}

/** Why `what` cannot be reached in this phase, and what was asked of it. */
export function unprovisioned(
  what: string,
  access: string,
  phase: UnprovisionedPhase,
): Error {
  return new Error(
    phase === "discovery"
      ? `'${what}' cannot be used during discovery: tried to access '${access}' before the resource was provisioned`
      : `'${what}' cannot be used while resources are suppressed: tried to access '${access}', and this deploy provisioned none`,
  );
}

/** A stand-in for `what`, throwing on the first property anything reads off it. */
export function unprovisionedProxy<T extends object>(
  what: string,
  phase: UnprovisionedPhase,
): T {
  return new Proxy({} as T, {
    get(_target, prop) {
      throw unprovisioned(what, String(prop), phase);
    },
  });
}
