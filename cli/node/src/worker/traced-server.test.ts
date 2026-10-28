import { execFile } from "node:child_process";
import { describe, expect, it } from "vitest";
import {
  attributes,
  bundled,
  clearExports,
  exports,
  named,
  parentSpanID,
  spansOf,
  tracedEnv,
  useCollector,
} from "../tracing/test-collector.js";

useCollector();

describe("a traced worker", () => {
  it("records each delivery a worker serves and sends what it holds when it is terminated", async () => {
    const env = { ...tracedEnv(), OCEL_TEST_WORKER: "media", PORT: "0" };
    clearExports();
    const entry = await bundled(`
      import { serve } from "@src/worker/traced-server.ts";
      const server = await serve(async (_worker, body) => ({ status: 200, body }), "OCEL_TEST_WORKER", [{ file: "workers/media.ts", load: async () => {} }]);
      server.on("listening", () => console.log("port " + server.address().port));
    `);
    const child = execFile(process.execPath, [entry], {
      env: { PATH: process.env.PATH ?? "", ...env },
    });
    const port = await new Promise<string>((resolve) =>
      child.stdout?.on("data", (chunk: Buffer) => {
        const match = /port (\d+)/.exec(chunk.toString());
        if (match?.[1]) resolve(match[1]);
      }),
    );
    const answer = await fetch(`http://127.0.0.1:${port}`, { method: "POST", body: `{"n":1}` });
    expect(answer.status).toBe(200);
    const exited = new Promise((resolve) => child.on("exit", (_code, signal) => resolve(signal)));
    child.kill("SIGTERM");

    expect(await exited).toBe("SIGTERM");
    const spans = exports.flatMap(spansOf);
    expect(attributes(named(spans, "deliver message"))).toMatchObject({
      "ocel.worker": "media",
      "http.request.body.size": 7,
      "http.response.status_code": 200,
    });
    expect(named(spans, "load worker").parentSpanId).toBe(parentSpanID);
    expect(attributes(named(spans, "load worker"))["ocel.import_count"]).toBe(1);
    expect(named(spans, "import module").parentSpanId).toBe(named(spans, "load worker").spanId);
    expect(attributes(named(spans, "import module"))["code.filepath"]).toBe("workers/media.ts");
  });
});
