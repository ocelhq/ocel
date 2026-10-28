import { describe, expect, it } from "vitest";
import {
  collectorToken,
  endpoint,
  exports,
  named,
  parentSpanID,
  tracedEnv,
  tracedRun,
  traceID,
  traceparent,
  useCollector,
} from "./test-collector.js";

useCollector();

describe("a node child started under a run's trace", () => {
  it("sends its discovery span to the collector as a child of the span TRACEPARENT names", async () => {
    const { code, spans } = await tracedRun(`
      import { discover } from "@src/discovery/runtime.ts";
      await discover([{ file: "declarations/main.ts", load: async () => {} }]);
    `);

    expect(code).toBe(0);
    const discovery = named(spans, "discover resources");
    expect(discovery.traceId).toBe(traceID);
    expect(discovery.parentSpanId).toBe(parentSpanID);
  });

  it("records its spans under the instrumentation scope of the package that records them", async () => {
    const { spans } = await tracedRun(`
      import { discover } from "@src/discovery/runtime.ts";
      await discover([{ file: "declarations/main.ts", load: async () => {} }]);
    `);

    expect(named(spans, "discover resources").scope).toBe("@cli/node");
  });

  it("sends nothing and runs the same when no TRACEPARENT names a parent", async () => {
    const env = tracedEnv();
    delete env.TRACEPARENT;
    const { code, stdout } = await tracedRun(
      `
      import { discover } from "@src/discovery/runtime.ts";
      await discover([{ file: "declarations/main.ts", load: async () => { console.log("loaded"); } }]);
    `,
      env,
    );

    expect(code).toBe(0);
    expect(stdout).toContain("loaded");
    expect(exports).toEqual([]);
  });

  it("sends OTLP protobuf carrying the run's token", async () => {
    const { spans } = await tracedRun(`
      import { discover } from "@src/discovery/runtime.ts";
      await discover([{ file: "declarations/main.ts", load: async () => {} }]);
    `);

    expect(spans.length).toBeGreaterThan(0);
    expect(exports.map((e) => e.contentType)).toEqual(exports.map(() => "application/x-protobuf"));
    expect(exports.map((e) => e.authorization)).toEqual(
      exports.map(() => `Bearer ${collectorToken}`),
    );
  });

  it("sends nothing when only the generic OTLP variables name a collector", async () => {
    const { code } = await tracedRun(
      `
      import { discover } from "@src/discovery/runtime.ts";
      await discover([{ file: "declarations/main.ts", load: async () => {} }]);
    `,
      {
        TRACEPARENT: traceparent,
        OTEL_EXPORTER_OTLP_ENDPOINT: endpoint,
        OTEL_EXPORTER_OTLP_TRACES_ENDPOINT: `${endpoint}/v1/traces`,
      },
    );

    expect(code).toBe(0);
    expect(exports).toEqual([]);
  });

  it("keeps the collector and its token from the modules it loads and the processes they spawn", async () => {
    const { stdout } = await tracedRun(`
      import { execFileSync } from "node:child_process";
      import { discover } from "@src/discovery/runtime.ts";
      await discover([{ file: "declarations/main.ts", load: async () => {
        console.log("module sees " + (process.env.OCEL_TRACE_COLLECTOR_TOKEN ?? "nothing"));
        console.log(execFileSync(process.execPath, ["-e", "console.log('child sees ' + (process.env.OCEL_TRACE_COLLECTOR_URL ?? 'nothing'))"]).toString().trim());
      } }]);
    `);

    expect(stdout).toContain("module sees nothing");
    expect(stdout).toContain("child sees nothing");
  });

  it("delivers its spans when a module it loads calls process.exit", async () => {
    const { code, spans } = await tracedRun(`
      import { discover } from "@src/discovery/runtime.ts";
      await discover([{ file: "declarations/main.ts", load: async () => process.exit(4) }]);
    `);

    expect(code).toBe(4);
    const discovery = named(spans, "discover resources");
    expect(discovery.parentSpanId).toBe(parentSpanID);
    expect(discovery.status?.code).toBe(2);
    expect(named(spans, "import module").parentSpanId).toBe(discovery.spanId);
  });

  it("records what a module throws on its import span as the span's error", async () => {
    const { spans } = await tracedRun(`
      import { discover } from "@src/discovery/runtime.ts";
      await discover([{ file: "declarations/main.ts", load: async () => { throw "no bucket named media"; } }]);
    `);

    const load = named(spans, "import module");
    expect(load.status).toEqual({ code: 2, message: "no bucket named media" });
    expect(load.events).toEqual(["exception"]);
  });

  it("leaves a signal the child handles itself to it, failing no open span and sending each span once", async () => {
    const { code, spans } = await tracedRun(`
      import { flushTracing, inSpan, startTracing } from "@src/tracing/tracing.ts";
      await startTracing("ocel-test");
      await inSpan("ended before the signal", {}, async () => {});
      await inSpan("serve", {}, () => new Promise((resolve) => {
        process.once("SIGTERM", () => resolve(undefined));
        process.kill(process.pid, "SIGTERM");
      }));
      await flushTracing();
    `);

    expect(code).toBe(0);
    expect(named(spans, "serve").status?.code ?? 0).toBe(0);
    expect(spans.filter((s) => s.name === "ended before the signal")).toHaveLength(1);
  });
});
