export function applyE2EEnvDefaults() {
  const databaseUrl =
    process.env.DATABASE_URL ??
    "postgres://postgres:postgres@localhost:5432/postgres";
  process.env.DATABASE_URL = databaseUrl;

  process.env.OCEL_RESOURCE_POSTGRES_main ??= JSON.stringify({
    connectionString: databaseUrl,
  });

  process.env.OCEL_CLOUD_ADMIN_URL ??=
    "postgres://postgres:postgres@localhost:5433/postgres";

  process.env.BETTER_AUTH_SECRET ??= "e2e-test-secret-not-for-production";
  process.env.BETTER_AUTH_URL ??= "http://localhost:3000";
}
