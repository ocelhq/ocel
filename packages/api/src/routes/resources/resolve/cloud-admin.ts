import { Client } from "pg";

export function getCloudAdminUrl(): URL {
  const raw = process.env.OCEL_CLOUD_ADMIN_URL;
  if (!raw) {
    throw new Error(
      "OCEL_CLOUD_ADMIN_URL is not set - cannot connect to the cloud cluster as admin",
    );
  }
  return new URL(raw);
}

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
