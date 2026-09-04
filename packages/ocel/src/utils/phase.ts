export type UnprovisionedPhase = "discovery" | "resources-suppressed";

/**
 * Thrown when app code reaches for a resource this run never provisioned — during
 * discovery, or under a deploy that suppressed resource declarations. Catch it to
 * keep a boot path alive when the resource is optional there; anything else thrown
 * from the same call means the resource exists and is genuinely broken.
 */
export class UnprovisionedResourceError extends Error {
  override name = "UnprovisionedResourceError";
}

export function unprovisionedPhase(): UnprovisionedPhase | undefined {
  const phase = process.env.OCEL_PHASE;
  return phase === "discovery" || phase === "resources-suppressed"
    ? phase
    : undefined;
}

export function unprovisioned(
  what: string,
  access: string,
  phase: UnprovisionedPhase,
): UnprovisionedResourceError {
  return new UnprovisionedResourceError(
    phase === "discovery"
      ? `'${what}' cannot be used during discovery: tried to access '${access}' before the resource was provisioned`
      : `'${what}' cannot be used while resources are suppressed: tried to access '${access}', and this deploy provisioned none`,
  );
}

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
