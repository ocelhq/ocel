import { availableParallelism } from "node:os";
import path from "node:path";
import { buildNext } from "@framework/next-build";
import { traceFunction } from "@framework/node-build/trace";
import { buildSvelteKit } from "@framework/sveltekit-build";
import { reportFailure } from "../protocol.js";
import { flushTracing, inSpan, startTracing } from "../tracing/tracing.js";
import { type AppBuild, buildApps } from "./apps.js";

const MAX_CONCURRENT_BUILDS = 4;

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
  await startTracing("ocel-build");
  const req = await inSpan("read build request", {}, async (span) => {
    const read = await readRequest();
    span.setAttributes({ "ocel.app_count": read.apps.length });
    return read;
  });
  await buildApps(
    req.apps,
    {
      next: (app, log) => buildNext(app, adapterPath, log),
      sveltekit: buildSvelteKit,
      node: traceFunction,
    },
    Math.min(MAX_CONCURRENT_BUILDS, Math.floor(availableParallelism() / 2)),
  );
}

main()
  .catch((err) => {
    reportFailure(err);
    process.exitCode = 1;
  })
  .finally(flushTracing);
