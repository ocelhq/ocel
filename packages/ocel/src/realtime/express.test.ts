import { request as requestHTTP } from "node:http";
import type { AddressInfo } from "node:net";
import express from "express";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { bindingKey } from "../binding/binding.js";
import { BindingType } from "../gen/proto/common/bindings/v1/bindings_pb.js";
import { appsyncBinding, readClaims } from "../testing/realtime-gateway.js";

vi.mock("../runtime/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
}));

const { realtime, createRealtimeHandler } = await import("./express.js");

const seen: Request[] = [];
const rt = realtime("app", {
  authorize: async (request) => {
    seen.push(request);
    const body = (await request.json()) as { ops: unknown[] };
    return { id: request.headers.get("x-user") ?? "nobody", ops: body.ops.length };
  },
  channels: {
    "orders/:orderId": { schema: z.object({ status: z.string() }), subscribe: () => true },
  },
});

const batch = {
  ops: [{ op: "subscribe", pattern: "orders/:orderId", params: { orderId: "o1" } }],
};

async function serveExpress(app: express.Express) {
  const server = app.listen(0, "127.0.0.1");
  await new Promise((resolve) => server.once("listening", resolve));
  const { port } = server.address() as AddressInfo;
  return {
    origin: `http://127.0.0.1:${port}`,
    close: () => new Promise((resolve) => server.close(resolve)),
  };
}

describe("the realtime handler on express", () => {
  beforeEach(() => {
    seen.length = 0;
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv(bindingKey("app", BindingType.REALTIME), appsyncBinding);
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it.each([
    ["without a body parser", false],
    ["behind express.json()", true],
  ])("serve express %s, handing authorize a Web Request", async (_, parsesJson) => {
    const app = express();
    if (parsesJson) app.use(express.json());
    app.post("/api/realtime", createRealtimeHandler(rt));
    const server = await serveExpress(app);

    try {
      const res = await fetch(`${server.origin}/api/realtime`, {
        method: "POST",
        headers: { "content-type": "application/json", "x-user": "u1", origin: server.origin },
        body: JSON.stringify(batch),
      });

      expect(res.status).toBe(200);
      expect(res.headers.get("cache-control")).toBe("no-store");
      const body = (await res.json()) as { grants: { wire: string; token: string }[] };
      expect(body.grants[0]?.wire).toBe("/app/orders/o1");
      expect(readClaims(body.grants[0]?.token ?? "").sub).toBe("u1");
      expect(seen[0]).toBeInstanceOf(Request);
      expect(new URL(seen[0]?.url ?? "").pathname).toBe("/api/realtime");
    } finally {
      await server.close();
    }
  });

  it.each([
    ["without a body parser", undefined],
    ["behind a body parser that took it", express.text({ type: "application/json", limit: "5mb" })],
  ])("refuses a body over 1 MiB with 413 %s", async (_, parser) => {
    const app = express();
    if (parser) app.use(parser);
    app.post("/api/realtime", createRealtimeHandler(rt));
    const server = await serveExpress(app);

    try {
      const res = await fetch(`${server.origin}/api/realtime`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: " ".repeat(2 * 1024 * 1024),
      });

      expect(res.status).toBe(413);
      expect(res.headers.get("cache-control")).toBe("no-store");
      expect(seen).toEqual([]);
    } finally {
      await server.close();
    }
  });

  it("answers 413 for a body over 1 MiB before the upload ends", async () => {
    const app = express();
    app.post("/api/realtime", createRealtimeHandler(rt));
    const server = await serveExpress(app);

    try {
      const status = await new Promise<number | undefined>((resolve, reject) => {
        const upload = requestHTTP(`${server.origin}/api/realtime`, {
          method: "POST",
          headers: { "content-type": "application/json", "transfer-encoding": "chunked" },
        });
        upload.on("response", (res) => {
          resolve(res.statusCode);
          upload.destroy();
        });
        upload.on("error", reject);
        upload.write(" ".repeat(2 * 1024 * 1024));
      });

      expect(status).toBe(413);
    } finally {
      await server.close();
    }
  });
});
