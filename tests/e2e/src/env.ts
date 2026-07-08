// Central defaults so the e2e suite runs locally against the root
// docker-compose stack with zero configuration, while every value stays
// overridable from the environment (as CI sets them explicitly).
//
// Unlike the other packages' test configs, the e2e suite must talk to the
// SAME control-plane database that the running apps/web server uses (that's
// where seed writes the user/org and where resolve reads project membership),
// so these default to the primary `postgres` DB rather than an isolated
// `ocelhq_test` one.
export function applyE2EEnvDefaults() {
  const databaseUrl =
    process.env.DATABASE_URL ??
    "postgres://postgres:postgres@localhost:5432/postgres";
  process.env.DATABASE_URL = databaseUrl;

  // @repo/db dogfoods the ocel SDK for its own connection, reading this var
  // rather than DATABASE_URL directly (normally injected by `ocel dev`).
  process.env.OCEL_RESOURCE_POSTGRES_main ??= JSON.stringify({
    connectionString: databaseUrl,
  });

  // The local "cloud" cluster admin connection (docker-compose `ocel-cloud`).
  process.env.OCEL_CLOUD_ADMIN_URL ??=
    "postgres://postgres:postgres@localhost:5433/postgres";

  // Better Auth signs session material with this; the seed and the running
  // apps/web server MUST share it for minted bearer tokens to validate.
  process.env.BETTER_AUTH_SECRET ??= "e2e-test-secret-not-for-production";
  process.env.BETTER_AUTH_URL ??= "http://localhost:3000";
}
