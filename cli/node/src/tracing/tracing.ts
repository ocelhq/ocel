import { type Attributes, type InSpan, type OpenSpan, untraced } from "./spans.js";

export interface Tracing {
  inSpan<T>(name: string, attributes: Attributes, fn: (span: OpenSpan) => Promise<T>): Promise<T>;
  flush(): Promise<void>;
}

let tracing: Tracing | undefined;

export const collectorURLEnv = "OCEL_TRACE_COLLECTOR_URL";
export const collectorTokenEnv = "OCEL_TRACE_COLLECTOR_TOKEN";
export const traceModeEnv = "OCEL_TRACE";

export async function startTracing(service: string): Promise<void> {
  if (tracing) return;
  const url = process.env[collectorURLEnv];
  const token = process.env[collectorTokenEnv] ?? "";
  const traceparent = process.env.TRACEPARENT;
  delete process.env[collectorURLEnv];
  delete process.env[collectorTokenEnv];
  delete process.env.TRACEPARENT;
  if (!url || !traceparent) return;
  const { startExport } = await import("./export.js");
  tracing ??= startExport(service, { url: new URL(url), token }, traceparent, {
    recordsSuccessfulBodies: process.env[traceModeEnv] === "full",
  });
}

export const inSpan: InSpan = (name, attributes, fn) =>
  tracing ? tracing.inSpan(name, attributes, fn) : untraced(name, attributes, fn);

export function flushTracing(): Promise<void> {
  return tracing?.flush() ?? Promise.resolve();
}
