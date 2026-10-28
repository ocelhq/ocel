import { tmpdir } from "node:os";
import { describe, expect, it } from "vitest";
import {
  attributes,
  named,
  tracedEnv,
  tracedRun,
  traceID,
  useCollector,
} from "./test-collector.js";

useCollector();

describe("a traced node child's processes and requests", () => {
  it("records a child process it spawns with its argv, directory and exit code under the span that spawned it", async () => {
    const { spans } = await tracedRun(`
      import { spawn } from "node:child_process";
      import { discover } from "@src/discovery/runtime.ts";
      await discover([
        { file: "declarations/main.ts", load: () =>
          new Promise((resolve) => {
            spawn(process.execPath, ["-e", "process.exit(3)"], { cwd: ${JSON.stringify(tmpdir())} }).on("exit", resolve);
          }) },
      ]);
    `);

    const load = named(spans, "import module");
    const child = named(spans, "node");
    expect(child.parentSpanId).toBe(load.spanId);
    expect(child.traceId).toBe(traceID);
    expect(attributes(child)).toMatchObject({
      "process.executable.path": process.execPath,
      "process.command_args": [process.execPath, "-e", "process.exit(3)"],
      "process.working_directory": tmpdir(),
      "process.exit.code": 3,
    });
    expect(child.status?.code).toBe(2);
  });

  it("starts a child process with the environment its caller set, naming no span of the run's trace", async () => {
    const callerTraceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01";
    const { stdout, spans } = await tracedRun(`
      import { execFile } from "node:child_process";
      import { discover } from "@src/discovery/runtime.ts";
      const traceparentOf = (env) =>
        new Promise((resolve) =>
          execFile(process.execPath, ["-e", "console.log(process.env.TRACEPARENT ?? 'nothing')"], { env }, (_e, out) => resolve(out.trim())),
        );
      await discover([
        { file: "declarations/main.ts", load: async () => {
          console.log("inherited " + (await traceparentOf(undefined)));
          console.log("given " + (await traceparentOf({ ...process.env, TRACEPARENT: ${JSON.stringify(callerTraceparent)} })));
        } },
      ]);
    `);

    expect(spans.filter((s) => s.name === "node")).toHaveLength(2);
    expect(stdout).toContain("inherited nothing");
    expect(stdout).toContain(`given ${callerTraceparent}`);
  });

  it("records the argv a child process is handed and the environment its caller changed", async () => {
    const { spans } = await tracedRun(`
      import { spawn } from "node:child_process";
      import { discover } from "@src/discovery/runtime.ts";
      await discover([
        { file: "declarations/main.ts", load: () =>
          new Promise((resolve) => {
            spawn(process.execPath, ["-e", "", "--region", "eu"], {
              env: { ...process.env, STAGE: "prod" },
            }).on("exit", resolve);
          }) },
      ]);
    `);

    const child = attributes(named(spans, "node"));
    expect(child["process.command_args"]).toEqual([process.execPath, "-e", "", "--region", "eu"]);
    expect(child["process.env.STAGE"]).toBe("prod");
    expect(child).not.toHaveProperty("process.env.PATH");
  });

  it("records an HTTP request and a fetch with what they sent and got back", async () => {
    const { spans } = await tracedRun(`
      import { createServer, get } from "node:http";
      import { discover } from "@src/discovery/runtime.ts";
      const statuses = { "/plain?page=2": 200, "/gone": 410, "/missing": 404 };
      const server = createServer((req, res) => res.writeHead(statuses[req.url]).end("ok"));
      await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
      const origin = "http://127.0.0.1:" + server.address().port;
      const read = (path) =>
        new Promise((resolve) => {
          get(origin + path, { headers: { "x-request-id": "r-1" } }, (res) => {
            res.resume();
            res.on("end", resolve);
          });
        });
      await discover([
        { file: "declarations/main.ts", load: () => read("/plain?page=2") },
        { file: "declarations/gone.ts", load: () => read("/gone") },
        { file: "declarations/fetch.ts", load: async () => {
          const res = await fetch(origin + "/missing", { headers: { "x-request-id": "r-2" } });
          await res.text();
        } },
      ]);
      server.close();
    `);

    const requests = spans.filter((s) => s.name.startsWith("GET 127.0.0.1:"));
    expect(requests).toHaveLength(3);
    const [plain, gone, missing] = requests;
    expect(attributes(plain as ReceivedSpan)).toMatchObject({
      "http.request.method": "GET",
      "url.full": expect.stringMatching(/^http:\/\/127\.0\.0\.1:\d+\/plain\?page=2$/),
      "http.request.header.x-request-id": ["r-1"],
      "http.response.status_code": 200,
    });
    expect(gone?.status).toEqual({ code: 2, message: "410 Gone" });
    expect(attributes(missing as ReceivedSpan)).toMatchObject({
      "url.full": expect.stringMatching(/\/missing$/),
      "http.request.header.x-request-id": ["r-2"],
      "http.response.status_code": 404,
    });
    expect(missing?.status).toEqual({ code: 2, message: "404 Not Found" });
    const imports = spans.filter((s) => s.name === "import module").map((s) => s.spanId);
    expect(requests.map((s) => s.parentSpanId).sort()).toEqual(imports.sort());
  });

  it("records the size of what each HTTP request and fetch got back, and what a failed one sent and got", async () => {
    const { spans } = await tracedRun(`
      import { createServer, request } from "node:http";
      import { discover } from "@src/discovery/runtime.ts";
      const json = { "content-type": "application/json" };
      const answers = {
        "/ok": [200, json, "ok"],
        "/refused": [422, json, '{"error":"bucket exists"}'],
        "/token": [400, json, '{"error":"invalid_client"}'],
        "/large": [500, { "content-type": "text/plain" }, "x".repeat(10000)],
        "/image": [500, { "content-type": "image/png" }, "png"],
      };
      const server = createServer((req, res) => {
        req.resume();
        req.on("end", () => {
          const [status, headers, body] = answers[req.url];
          res.writeHead(status, headers).end(body);
        });
      });
      await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
      const origin = "http://127.0.0.1:" + server.address().port;
      const send = (path, type, ...chunks) =>
        new Promise((resolve) => {
          const req = request(origin + path, { method: "POST", headers: { "content-type": type } }, (res) => {
            res.resume();
            res.on("end", resolve);
          });
          for (const chunk of chunks.slice(0, -1)) req.write(chunk);
          req.end(chunks.at(-1));
        });
      await discover([
        { file: "declarations/main.ts", load: async () => {
          await send("/ok", "application/json", '{"name":', '"media"}');
          await send("/refused", "application/json", '{"name":', '"media"}');
          await send("/large", "text/plain", "large");
          await send("/image", "image/png", "png");
          const res = await fetch(origin + "/refused", { method: "POST", headers: json, body: '{"name":"media"}' });
          await res.text();
          const token = await fetch(origin + "/token", {
            method: "POST",
            headers: { "content-type": "application/x-www-form-urlencoded" },
            body: "grant_type=client_credentials&client_secret=s3cret",
          });
          await token.text();
        } },
      ]);
      server.close();
    `);

    const sentTo = (path: string) =>
      spans.filter((s) => String(attributes(s)["url.full"]).endsWith(path)).map(attributes);
    const [ok] = sentTo("/ok");
    expect(ok).toMatchObject({ "http.response.body.size": 2 });
    expect(ok).not.toHaveProperty("http.request.body");
    expect(ok).not.toHaveProperty("http.response.body");
    expect(sentTo("/refused")).toEqual([
      expect.objectContaining({
        "http.request.body": '{"name":"media"}',
        "http.request.body.truncated": false,
        "http.response.body": '{"error":"bucket exists"}',
        "http.response.body.truncated": false,
        "http.response.body.size": 25,
      }),
      expect.objectContaining({
        "http.request.body": '{"name":"media"}',
        "http.response.body": '{"error":"bucket exists"}',
        "http.response.body.size": 25,
      }),
    ]);
    const [large] = sentTo("/large");
    expect(large).toMatchObject({
      "http.response.body.size": 10000,
      "http.response.body": "x".repeat(8192),
      "http.response.body.truncated": true,
    });
    const [image] = sentTo("/image");
    expect(image).toMatchObject({ "http.response.body.size": 3 });
    expect(image).not.toHaveProperty("http.request.body");
    expect(image).not.toHaveProperty("http.response.body");
    const [token] = sentTo("/token");
    expect(token).toMatchObject({ "http.response.body.size": 26 });
    expect(token).not.toHaveProperty("http.request.body");
    expect(token).not.toHaveProperty("http.response.body");
  });

  it("records what a successful HTTP request and fetch sent and got back under a full trace, and still never a grant's", async () => {
    const { spans } = await tracedRun(
      `
      import { createServer, request } from "node:http";
      import { discover } from "@src/discovery/runtime.ts";
      const server = createServer((req, res) => {
        req.resume();
        req.on("end", () => res.writeHead(200, { "content-type": "application/json" }).end('{"id":"media"}'));
      });
      await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
      const origin = "http://127.0.0.1:" + server.address().port;
      await discover([
        { file: "declarations/main.ts", load: async () => {
          await new Promise((resolve) => {
            const req = request(origin + "/http", { method: "POST", headers: { "content-type": "application/json" } }, (res) => {
              res.resume();
              res.on("end", resolve);
            });
            req.end('{"name":"media"}');
          });
          const res = await fetch(origin + "/fetch", { method: "POST", headers: { "content-type": "application/json" }, body: '{"name":"media"}' });
          await res.text();
          const token = await fetch(origin + "/token", {
            method: "POST",
            headers: { "content-type": "application/x-www-form-urlencoded" },
            body: "grant_type=client_credentials&client_secret=s3cret",
          });
          await token.text();
        } },
      ]);
      server.close();
    `,
      { ...tracedEnv(), OCEL_TRACE: "full" },
    );

    const sentTo = (path: string) =>
      spans.filter((s) => String(attributes(s)["url.full"]).endsWith(path)).map(attributes);
    for (const path of ["/http", "/fetch"]) {
      expect(sentTo(path)).toEqual([
        expect.objectContaining({
          "http.request.body": '{"name":"media"}',
          "http.response.body": '{"id":"media"}',
        }),
      ]);
    }
    const [token] = sentTo("/token");
    expect(token).not.toHaveProperty("http.request.body");
    expect(token).not.toHaveProperty("http.response.body");
  });
});
