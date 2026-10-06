/** A phase in which app code runs before any resource is provisioned. */
export type UnprovisionedPhase = "discovery" | "build";

/**
 * Thrown when app code reaches for a resource this run never provisioned: during
 * discovery, the pass that reads the declarations before anything is provisioned, or
 * while the app is being built, which imports its modules before anything is
 * provisioned. Catch it to keep a boot path alive when the resource is optional there;
 * anything else thrown from the same call means the resource exists and is genuinely
 * broken.
 */
export class UnprovisionedResourceError extends Error {
  override name = "UnprovisionedResourceError";
}

const nextProductionBuild = "phase-production-build";

/** The phase this process runs in when no resource has been provisioned yet; undefined once resources are. */
export function unprovisionedPhase(): UnprovisionedPhase | undefined {
  if (process.env.OCEL_PHASE === "discovery") return "discovery";
  if (process.env.NEXT_PHASE === nextProductionBuild) return "build";
  return undefined;
}

/** The error app code gets for touching `access` on the resource `what` before it was provisioned. */
export function unprovisioned(what: string, access: string): UnprovisionedResourceError {
  if (unprovisionedPhase() === "build") {
    return new UnprovisionedResourceError(
      `'${what}' cannot be used while the app is being built: tried to access '${access}', and resources are provisioned after the build; a page or route that reads it at build time must be dynamic`,
    );
  }
  return new UnprovisionedResourceError(
    `'${what}' cannot be used during discovery: tried to access '${access}' before the resource was provisioned`,
  );
}

/** A stand-in for the resource `what` that throws {@link UnprovisionedResourceError} on any property access. */
export function unprovisionedProxy<T extends object>(what: string): T {
  return new Proxy({} as T, {
    get(_target, prop) {
      throw unprovisioned(what, String(prop));
    },
  });
}
