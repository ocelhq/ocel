import { spawn } from "node:child_process";

const databaseUrl = process.env.DATABASE_URL;
if (!databaseUrl) {
  console.error(
    "DATABASE_URL is not set. Set it in console/web/.env.local (see .env.example) before running `pnpm dev`.",
  );
  process.exit(1);
}

process.env.OCEL_RESOURCE_POSTGRES_main = JSON.stringify({
  connectionString: databaseUrl,
});

const extraArgs = process.argv.slice(2);
const child = spawn("next", ["dev", ...extraArgs], {
  stdio: "inherit",
  env: process.env,
});

process.on("SIGTERM", () => child.kill("SIGTERM"));

child.on("exit", (code, signal) => {
  if (signal) {
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code ?? 0);
});

child.on("error", (err) => {
  console.error(`Failed to start \`next dev\`: ${err.message}`);
  process.exit(1);
});
