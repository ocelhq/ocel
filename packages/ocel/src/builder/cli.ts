import { buildApps } from "./build.js";
import type { AppInput, BuildOptions } from "./types.js";

/**
 * Runnable entry the CLI bundle exposes. Reads a build request as JSON from
 * argv[2] or stdin: `{ outDir, apps: AppInput[] }`. Emits the functions[]
 * contract as a single JSON object to stdout: `{ "functions": [...] }`.
 */
interface BuildRequest extends BuildOptions {
  apps: AppInput[];
}

async function readRequest(): Promise<BuildRequest> {
  const arg = process.argv[2];
  if (arg) return JSON.parse(arg) as BuildRequest;
  const chunks: Buffer[] = [];
  for await (const chunk of process.stdin) chunks.push(chunk as Buffer);
  return JSON.parse(Buffer.concat(chunks).toString("utf8")) as BuildRequest;
}

async function main(): Promise<void> {
  const req = await readRequest();
  const functions = await buildApps(req.apps, { outDir: req.outDir });
  process.stdout.write(`${JSON.stringify({ functions })}\n`);
}

main().catch((err) => {
  process.stderr.write(`${err instanceof Error ? err.stack : String(err)}\n`);
  process.exit(1);
});
