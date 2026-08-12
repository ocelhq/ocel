import { buildApps, detectApp, writeBuildPlan } from "./build.js";
import type { AppInput, BuildOptions } from "./types.js";

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
  await writeBuildPlan(req.outDir, await buildApps(apps, { outDir: req.outDir }));
}

main().catch((err) => {
  process.stderr.write(`${err instanceof Error ? err.stack : String(err)}\n`);
  process.exit(1);
});
