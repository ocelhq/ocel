// Direct-inject dev wrapper for apps/web.
//
// apps/web *is* the Ocel control plane, so it can't reach back through
// `ocel dev` into an API it is itself starting. Instead we inject the one
// resource env var the SDK contract needs directly: for postgres("main"),
// OCEL_RESOURCE_POSTGRES_main is a JSON {connectionString} whose value is
// just this app's own control-plane DATABASE_URL. Then we run `next dev`
// verbatim. No CLI, no API, no provisioning handshake.
import { spawn } from "node:child_process";

const databaseUrl = process.env.DATABASE_URL;
if (!databaseUrl) {
  console.error(
    "DATABASE_URL is not set. Set it in apps/web/.env.local (see .env.example) before running `pnpm dev`.",
  );
  process.exit(1);
}

process.env.OCEL_RESOURCE_POSTGRES_main = JSON.stringify({
  connectionString: databaseUrl,
});

// Forward any extra args after the script through to `next dev`.
const extraArgs = process.argv.slice(2);
const child = spawn("next", ["dev", ...extraArgs], {
  stdio: "inherit",
  env: process.env,
});

// Forward SIGTERM so process managers / `kill` can stop `next dev`.
// SIGINT (Ctrl+C) is already delivered by the OS to the whole process
// group, so forwarding it would send a second SIGINT to the child.
process.on("SIGTERM", () => child.kill("SIGTERM"));

child.on("exit", (code, signal) => {
  if (signal) {
    // Re-raise the signal so our exit status reflects how the child died.
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code ?? 0);
});

child.on("error", (err) => {
  console.error(`Failed to start \`next dev\`: ${err.message}`);
  process.exit(1);
});
