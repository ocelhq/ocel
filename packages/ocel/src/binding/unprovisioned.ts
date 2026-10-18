/**
 * Thrown when app code reaches for a resource this run never provisioned, which
 * is discovery: the pass that reads the declarations before anything is
 * provisioned. Catch it to keep a boot path alive when the resource is optional there; anything else
 * thrown from the same call means the resource exists and is genuinely broken.
 */
export class UnprovisionedResourceError extends Error {
  override name = "UnprovisionedResourceError";
}

export function isDiscovering(): boolean {
  return process.env.OCEL_PHASE === "discovery";
}

export function unprovisioned(what: string, access: string): UnprovisionedResourceError {
  return new UnprovisionedResourceError(
    `'${what}' cannot be used during discovery: tried to access '${access}' before the resource was provisioned`,
  );
}
