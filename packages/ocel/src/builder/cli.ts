import { buildApps, detectApp } from "./build.js";
import type { AppInput, BuildOptions } from "./types.js";

/**
 * Runnable entry the CLI resolves via OCEL_BUILDER_PATH. Reads a build request
 * as JSON from argv[2] or stdin: `{ outDir, projectRoot, apps }`. With apps it
 * builds each; with none it detects a single app at projectRoot. It writes
 * nothing to stdout — the Go CLI discovers built functions by walking outDir.
 */
interface BuildRequest extends BuildOptions {
  projectRoot: string;
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
  const detected = req.apps.length === 0 ? detectApp(req.projectRoot) : undefined;
  const apps = detected ? [detected] : req.apps;
  await buildApps(apps, { outDir: req.outDir });
}

main().catch((err) => {
  process.stderr.write(`${err instanceof Error ? err.stack : String(err)}\n`);
  process.exit(1);
});
