import { AsyncLocalStorage } from "node:async_hooks";
import { spawnSync } from "node:child_process";
import { request as httpRequest } from "node:http";
import { request as httpsRequest } from "node:https";
import {
  type Context,
  defaultTextMapGetter,
  ROOT_CONTEXT,
  type Span,
  SpanStatusCode,
  trace,
} from "@opentelemetry/api";
import { W3CTraceContextPropagator } from "@opentelemetry/core";
import { ProtobufTraceSerializer } from "@opentelemetry/otlp-transformer";
import { resourceFromAttributes } from "@opentelemetry/resources";
import { type ReadableSpan, type SpanProcessor, TracerProvider } from "@opentelemetry/sdk-trace";
import { failSpan, recordChildProcesses, recordFetches, recordHTTPRequests } from "./channels.js";
import type { Attributes, OpenSpan } from "./spans.js";
import { collectorTokenEnv, collectorURLEnv, type Tracing } from "./tracing.js";

const exportDelayMs = 100;
const exportTimeoutMs = 2_000;
const maxPendingSpans = 512;

const postStdin = `
const chunks = [];
process.stdin.on("data", (c) => chunks.push(c)).on("end", () => {
  const url = new URL(process.env.${collectorURLEnv});
  const { request } = require(url.protocol === "https:" ? "node:https" : "node:http");
  const headers = { "content-type": "application/x-protobuf", authorization: "Bearer " + process.env.${collectorTokenEnv} };
  const req = request(url, { method: "POST", headers }, (res) => res.resume());
  req.on("error", () => {});
  req.end(Buffer.concat(chunks));
});
`;

interface Collector {
  url: URL;
  token: string;
}

function serialize(spans: ReadableSpan[]): Uint8Array {
  return ProtobufTraceSerializer.serializeRequest(spans) ?? new Uint8Array();
}

const exporting = new AsyncLocalStorage<true>();

function post(collector: Collector, body: Uint8Array): Promise<void> {
  const send = collector.url.protocol === "https:" ? httpsRequest : httpRequest;
  return new Promise((resolve) => {
    exporting.run(true, () => {
      const req = send(
        collector.url,
        {
          method: "POST",
          agent: false,
          timeout: exportTimeoutMs,
          headers: {
            "content-type": "application/x-protobuf",
            "content-length": body.byteLength,
            authorization: `Bearer ${collector.token}`,
          },
        },
        (res) => {
          res.resume();
          res.once("end", resolve);
          res.once("error", () => resolve());
        },
      );
      req.once("error", () => resolve());
      req.once("timeout", () => req.destroy());
      req.end(body);
    });
  });
}

class CollectorSpans implements SpanProcessor {
  readonly open = new Set<Span>();
  private ended: ReadableSpan[] = [];
  private sending = new Set<Promise<void>>();
  private unsent = new Set<Uint8Array>();
  private timer: NodeJS.Timeout | undefined;

  constructor(private readonly collector: Collector) {}

  onStart(span: Span): void {
    this.open.add(span);
  }

  onEnd(span: ReadableSpan): void {
    this.open.delete(span as unknown as Span);
    this.ended.push(span);
    if (this.ended.length >= maxPendingSpans) {
      this.send();
      return;
    }
    this.timer ??= setTimeout(() => this.send(), exportDelayMs).unref();
  }

  get hasEnded(): boolean {
    return this.ended.length > 0;
  }

  sendBeforeExit(): void {
    for (const span of this.open) {
      span.setStatus({
        code: SpanStatusCode.ERROR,
        message: "the process exited before this span ended",
      });
      span.end();
    }
    const spans = this.takeEnded();
    const bodies = [...this.unsent];
    this.unsent.clear();
    if (spans.length > 0) bodies.push(serialize(spans));
    if (bodies.length === 0) return;
    spawnSync(process.execPath, ["-e", postStdin], {
      input: Buffer.concat(bodies),
      timeout: exportTimeoutMs,
      stdio: ["pipe", "ignore", "ignore"],
      env: {
        [collectorURLEnv]: this.collector.url.href,
        [collectorTokenEnv]: this.collector.token,
      },
    });
  }

  takeEnded(): ReadableSpan[] {
    clearTimeout(this.timer);
    this.timer = undefined;
    return this.ended.splice(0);
  }

  send(): void {
    const spans = this.takeEnded();
    if (spans.length === 0) return;
    const body = serialize(spans);
    this.unsent.add(body);
    const sent = post(this.collector, body).finally(() => {
      this.unsent.delete(body);
      this.sending.delete(sent);
    });
    this.sending.add(sent);
  }

  async forceFlush(): Promise<void> {
    this.send();
    await Promise.all(this.sending);
  }

  shutdown(): Promise<void> {
    return this.forceFlush();
  }
}

function definedAttributes(attributes: Attributes) {
  return Object.fromEntries(
    Object.entries(attributes).filter(([, v]) => v !== undefined),
  ) as Record<string, string | number | boolean | string[]>;
}

export interface ExportOptions {
  recordsSuccessfulBodies: boolean;
}

export function startExport(
  service: string,
  collector: Collector,
  traceparent: string,
  { recordsSuccessfulBodies }: ExportOptions,
): Tracing {
  const spans = new CollectorSpans(collector);
  const provider = new TracerProvider({
    resource: resourceFromAttributes({ "service.name": service }),
    spanProcessors: [spans],
  });
  const tracer = provider.getTracer("@cli/node");
  const root = new W3CTraceContextPropagator().extract(
    ROOT_CONTEXT,
    { traceparent },
    defaultTextMapGetter,
  );
  const active = new AsyncLocalStorage<Context>();
  const currentContext = () => active.getStore() ?? root;
  const recording = {
    tracer,
    currentContext,
    isExporting: () => exporting.getStore() === true,
    recordsSuccessfulBodies,
  };
  recordChildProcesses(recording);
  recordHTTPRequests(recording);
  recordFetches(recording);

  const flush = async () => {
    let timer: NodeJS.Timeout | undefined;
    await Promise.race([
      spans.forceFlush(),
      new Promise<void>((resolve) => {
        timer = setTimeout(resolve, exportTimeoutMs).unref();
      }),
    ]);
    clearTimeout(timer);
  };

  process.on("beforeExit", () => {
    if (spans.hasEnded) void flush();
  });
  process.on("exit", () => spans.sendBeforeExit());
  for (const signal of ["SIGINT", "SIGTERM"] as const) {
    process.once(signal, () => {
      if (process.listenerCount(signal) > 0) {
        spans.send();
        return;
      }
      spans.sendBeforeExit();
      process.kill(process.pid, signal);
    });
  }

  return {
    async inSpan(name, attributes, fn) {
      const parent = currentContext();
      const span = tracer.startSpan(name, { attributes: definedAttributes(attributes) }, parent);
      const open: OpenSpan = { setAttributes: (a) => span.setAttributes(definedAttributes(a)) };
      try {
        return await active.run(trace.setSpan(parent, span), () => fn(open));
      } catch (err) {
        failSpan(span, err);
        throw err;
      } finally {
        span.end();
      }
    },
    flush,
  };
}
