import { createServer, type Server } from "node:http";
import { type InSpan, importModules, type ModuleImport, untraced } from "../tracing/spans.js";

export type Deliver = (
  worker: string,
  body: string,
  signal: AbortSignal,
) => Promise<{ status: number; body: string }>;

export async function serve(
  deliver: Deliver,
  workerVar: string,
  imports: ReadonlyArray<ModuleImport>,
  inSpan: InSpan = untraced,
): Promise<Server> {
  const worker = process.env[workerVar];
  if (!worker) {
    console.error(`ocel: ${workerVar} names no worker, so this worker entry has nothing to serve`);
    process.exit(1);
  }
  await inSpan("load worker", { "ocel.worker": worker, "ocel.import_count": imports.length }, () =>
    importModules(imports, inSpan),
  );
  return createServer(async (req, res) => {
    if (req.method !== "POST") {
      res.writeHead(405, { allow: "POST" }).end();
      return;
    }
    await inSpan("deliver message", { "ocel.worker": worker }, async (span) => {
      const chunks: Buffer[] = [];
      for await (const chunk of req) chunks.push(chunk);
      const body = Buffer.concat(chunks);
      span.setAttributes({ "http.request.body.size": body.byteLength });
      const canceled = new AbortController();
      res.on("close", () => {
        if (!res.writableFinished) canceled.abort();
      });
      const answer = await deliver(worker, body.toString("utf8"), canceled.signal);
      span.setAttributes({ "http.response.status_code": answer.status });
      const contentType =
        answer.status === 200 || answer.status === 422
          ? "application/json"
          : "text/plain; charset=utf-8";
      res.writeHead(answer.status, { "content-type": contentType }).end(answer.body);
    });
  }).listen(Number(process.env.PORT), process.env.HOST || "127.0.0.1");
}
