export const DEFAULT_DATABASE_URL = "postgres://postgres:postgres@localhost:5432/postgres";

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

export function applyConsoleEnvDefaults() {
  const databaseUrl = process.env.DATABASE_URL ?? DEFAULT_DATABASE_URL;
  process.env.DATABASE_URL = databaseUrl;
  process.env.OCEL_RESOURCE_POSTGRES_main ??= postgresLink("main", databaseUrl);
  process.env.OCEL_CLOUD_ADMIN_URL ??= "postgres://postgres:postgres@localhost:5433/postgres";
  process.env.BETTER_AUTH_SECRET ??= "journey-secret-not-for-production";
  process.env.BETTER_AUTH_URL ??= consoleUrl();
}

export function consoleUrl(): string {
  return process.env.OCEL_API_URL ?? "http://localhost:3000";
}
