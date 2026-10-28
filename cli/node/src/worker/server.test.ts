import { request } from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ModuleImport } from "../tracing/spans.js";
import { type Deliver, serve } from "./server.js";

const workerVar = "OCEL_TEST_WORKER";

let env: NodeJS.ProcessEnv;
const closers: Array<() => Promise<void>> = [];

beforeEach(() => {
  env = { ...process.env };
  process.env[workerVar] = "media";
  process.env.PORT = "0";
  process.env.HOST = "127.0.0.1";
});

afterEach(async () => {
  vi.restoreAllMocks();
  process.env = env;
  for (const close of closers.splice(0)) await close();
});

async function served(deliver: Deliver, imports: ReadonlyArray<ModuleImport> = []) {
  const server = await serve(deliver, workerVar, imports);
  closers.push(
    () =>
      new Promise((resolve) => {
        server.closeAllConnections();
        server.close(() => resolve());
      }),
  );
  if (!server.listening) await new Promise((resolve) => server.once("listening", resolve));
  return `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
}

describe("serve", () => {
  it("hands each POSTed body to the worker it names and answers JSON for a run's output", async () => {
    const deliver = vi.fn<Deliver>(async (worker, body) => ({
      status: 200,
      body: `{"worker":"${worker}","body":${body}}`,
    }));
    const url = await served(deliver);

    const answer = await fetch(url, { method: "POST", body: `{"n":1}` });

    expect(answer.status).toBe(200);
    expect(answer.headers.get("content-type")).toBe("application/json");
    expect(await answer.json()).toEqual({ worker: "media", body: { n: 1 } });
  });

  it("answers a failure without retry as JSON and any other answer as text", async () => {
    const statuses = [422, 500, 404];
    const url = await served(async () => ({ status: statuses.shift() ?? 0, body: "answer" }));

    const types = [];
    for (let i = 0; i < 3; i++) {
      const answer = await fetch(url, { method: "POST", body: "{}" });
      types.push([answer.status, answer.headers.get("content-type")]);
    }

    expect(types).toEqual([
      [422, "application/json"],
      [500, "text/plain; charset=utf-8"],
      [404, "text/plain; charset=utf-8"],
    ]);
  });

  it("refuses any method but POST with 405 and delivers nothing", async () => {
    const deliver = vi.fn<Deliver>();
    const url = await served(deliver);

    const answer = await fetch(url);

    expect(answer.status).toBe(405);
    expect(answer.headers.get("allow")).toBe("POST");
    expect(deliver).not.toHaveBeenCalled();
  });

  it("runs every import in order before it listens", async () => {
    const ran: string[] = [];
    const url = await served(
      async () => ({ status: 200, body: JSON.stringify(ran) }),
      [
        { file: "workers/a.ts", load: async () => ran.push("a") },
        { file: "workers/b.ts", load: async () => ran.push("b") },
      ],
    );

    expect(await (await fetch(url, { method: "POST", body: "{}" })).json()).toEqual(["a", "b"]);
  });

  it("aborts a run whose caller hung up", async () => {
    let aborted: (() => void) | undefined;
    const hungUp = new Promise<void>((resolve) => {
      aborted = resolve;
    });
    const url = await served(
      (_worker, _body, signal) =>
        new Promise((resolve) => {
          signal.addEventListener("abort", () => {
            aborted?.();
            resolve({ status: 500, body: "canceled" });
          });
        }),
    );

    const caller = request(url, { method: "POST" });
    caller.on("error", () => {});
    caller.end("{}", () => setTimeout(() => caller.destroy(), 50));

    await expect(hungUp).resolves.toBeUndefined();
  });

  it("exits with an error naming the variable when it names no worker, before any import runs", async () => {
    delete process.env[workerVar];
    const exit = vi.spyOn(process, "exit").mockImplementation((() => {
      throw new Error("exited");
    }) as never);
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    const load = vi.fn(async () => {});

    await expect(
      serve(vi.fn<Deliver>(), workerVar, [{ file: "workers/media.ts", load }]),
    ).rejects.toThrow("exited");

    expect(exit).toHaveBeenCalledWith(1);
    expect(errors).toHaveBeenCalledWith(
      `ocel: ${workerVar} names no worker, so this worker entry has nothing to serve`,
    );
    expect(load).not.toHaveBeenCalled();
  });
});
