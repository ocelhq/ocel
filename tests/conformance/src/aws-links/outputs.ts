import { splitIds, type LinkOutputs } from "../links";

export function outputs(values: Record<string, unknown>): LinkOutputs {
  for (const name of ["host", "port", "database"] as const) {
    if (values[name] === undefined || values[name] === "") {
      throw new Error(`the external stack returned no ${name}`);
    }
  }
  const subnetIds = splitIds(values.subnetIds);
  const securityGroupIds = splitIds(values.securityGroupIds);
  if (!subnetIds.length || !securityGroupIds.length) {
    throw new Error("the external stack returned no network placement");
  }
  return {
    host: String(values.host),
    port: String(values.port),
    database: String(values.database),
    subnetIds,
    securityGroupIds,
  };
}
