import { pathToFileURL } from "node:url";
import { fromReport, renderComment } from "./preview";
import { reportSchema } from "./report";

export function renderReport(payload: unknown): string {
  const report = reportSchema.parse(payload);
  return renderComment(report.phase, fromReport(report));
}

async function main(): Promise<void> {
  const chunks: Buffer[] = [];
  for await (const chunk of process.stdin) chunks.push(chunk as Buffer);
  process.stdout.write(renderReport(JSON.parse(Buffer.concat(chunks).toString("utf8"))));
}

const entry = process.argv[1];
if (entry && import.meta.url === pathToFileURL(entry).href) {
  await main();
}
