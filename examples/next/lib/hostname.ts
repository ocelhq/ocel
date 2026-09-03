export const APP = "web";

export function projectSlug(): string {
  const run = process.env.OCEL_JOURNEY_RUN;
  return run ? `j-${run}-next` : "next";
}

export function productionHostname(app: string): string | undefined {
  const zone = process.env.OCEL_JOURNEY_ZONE;
  return zone ? `${app}.${projectSlug()}.${zone}` : undefined;
}
