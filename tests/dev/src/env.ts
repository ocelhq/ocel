export function postgresLink(name: string, url: string): string {
  const parsed = new URL(url);
  return JSON.stringify({
    name,
    postgres: {
      host: parsed.hostname,
      port: Number(parsed.port || 5432),
      database: parsed.pathname.slice(1),
      username: decodeURIComponent(parsed.username),
      password: decodeURIComponent(parsed.password),
    },
  });
}

const nextEnvironmentKeys = [
  "STRIPE_API_KEY",
  "POSTHOG_PROJECT_ID",
  "NEXT_PUBLIC_APP_ID",
  "SOME_FOLDER_VALUE",
  "NEXT_PUBLIC_GA4_ID",
  "SUPER_SECRET_VALUE",
] as const;

export function nextDotenv(): string {
  return `${nextEnvironmentKeys
    .map((key) => `${key}=${process.env[key] ?? ""}`)
    .join("\n")}\n`;
}

export function applyDevEnvDefaults() {
  const databaseUrl =
    process.env.DATABASE_URL ??
    "postgres://postgres:postgres@localhost:5432/postgres";
  process.env.DATABASE_URL = databaseUrl;

  process.env.OCEL_RESOURCE_POSTGRES_main ??= postgresLink("main", databaseUrl);

  process.env.OCEL_CLOUD_ADMIN_URL ??=
    "postgres://postgres:postgres@localhost:5433/postgres";

  process.env.OCEL_BLOB_ENDPOINT ??= "http://localhost:9000";
  process.env.OCEL_BLOB_BUCKET ??= "ocel-dev";
  process.env.OCEL_BLOB_REGION ??= "us-east-1";
  process.env.OCEL_BLOB_ACCESS_KEY_ID ??= "minioadmin";
  process.env.OCEL_BLOB_SECRET_ACCESS_KEY ??= "minioadmin";

  process.env.STRIPE_API_KEY ??= "e2e-stripe-key";
  process.env.POSTHOG_PROJECT_ID ??= "e2e-posthog-project";
  process.env.NEXT_PUBLIC_APP_ID ??= "e2e-app";
  process.env.SOME_FOLDER_VALUE ??= "e2e-folder-value";
  process.env.NEXT_PUBLIC_GA4_ID ??= "e2e-ga4-id";
  process.env.SUPER_SECRET_VALUE ??= "e2e-secret-value";

  process.env.BETTER_AUTH_SECRET ??= "e2e-test-secret-not-for-production";
  process.env.BETTER_AUTH_URL ??= "http://localhost:3000";
}
