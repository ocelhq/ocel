import { Client } from "pg";

// The shared "cloud" cluster's admin connection - a docker-compose Postgres
// standing in for Aurora Serverless v2 in prod (see the epic's design
// decisions). First-request provisioning connects here to carve out a
// per-user role + database; resolving an existing assignment never needs it.
export function getCloudAdminUrl(): URL {
  const raw = process.env.OCEL_CLOUD_ADMIN_URL;
  if (!raw) {
    throw new Error(
      "OCEL_CLOUD_ADMIN_URL is not set - cannot connect to the cloud cluster as admin",
    );
  }
  return new URL(raw);
}

// Runs `fn` with a short-lived admin connection to the cloud cluster,
// closing it afterwards. Provisioning happens once per new assignment
// (subsequent resolves reuse the stored row and never call this), so a
// pooled connection isn't worth the complexity.
export async function withCloudAdminClient<T>(
  fn: (client: Client) => Promise<T>,
): Promise<T> {
  const client = new Client({
    connectionString: getCloudAdminUrl().toString(),
  });
  await client.connect();
  try {
    return await fn(client);
  } finally {
    await client.end();
  }
}
