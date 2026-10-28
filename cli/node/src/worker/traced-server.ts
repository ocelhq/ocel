import type { Server } from "node:http";
import type { ModuleImport } from "../tracing/spans.js";
import { inSpan, startTracing } from "../tracing/tracing.js";
import { type Deliver, serve as serveUntraced } from "./server.js";

export async function serve(
  deliver: Deliver,
  workerVar: string,
  imports: ReadonlyArray<ModuleImport>,
): Promise<Server> {
  await startTracing("ocel-worker");
  return serveUntraced(deliver, workerVar, imports, inSpan);
}
