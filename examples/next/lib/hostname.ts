export const APP = "web";

export function projectSlug(): string {
  return process.env.OCEL_JOURNEY_SLUG ?? "next";
}

export function productionHostname(app: string): string | undefined {
  const zone = process.env.OCEL_JOURNEY_ZONE;
  return zone ? `${app}-${projectSlug()}.${zone}` : undefined;
}
