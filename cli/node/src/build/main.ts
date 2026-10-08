import path from "node:path";
import { buildNext } from "@framework/next-build";
import { traceFunction } from "@framework/node-build/trace";
import { buildSvelteKit } from "@framework/sveltekit-build";
import { type AppBuild, buildApps } from "./apps.js";
import { reportFailure } from "./protocol.js";

interface BuildRequest {
  apps: AppBuild[];
}

async function readRequest(): Promise<BuildRequest> {
  const chunks: Buffer[] = [];
  for await (const chunk of process.stdin) chunks.push(chunk as Buffer);
  return JSON.parse(Buffer.concat(chunks).toString("utf8")) as BuildRequest;
}

async function main(): Promise<void> {
  const adapterPath = path.resolve(
    path.dirname(process.argv[1] ?? ""),
    "..",
    "next-adapter",
    "next-adapter.mjs",
  );
  const req = await readRequest();
  await buildApps(req.apps, {
    next: (app) => buildNext(app, adapterPath),
    sveltekit: buildSvelteKit,
    node: traceFunction,
  });
}

main().catch((err) => {
  reportFailure(err);
  process.exitCode = 1;
});
