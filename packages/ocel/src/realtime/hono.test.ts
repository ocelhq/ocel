import { Hono } from "hono";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { bindingKey } from "../binding/binding.js";
import { BindingType } from "../gen/proto/common/bindings/v1/bindings_pb.js";
import { appsyncBinding, readClaims } from "../testing/realtime-gateway.js";

vi.mock("../runtime/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
}));

const { realtime, createRealtimeHandler } = await import("./hono.js");

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

describe("the realtime handler on hono", () => {
  beforeEach(() => {
    seen.length = 0;
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv(bindingKey("app", BindingType.REALTIME), appsyncBinding);
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("serves hono, handing authorize a Web Request", async () => {
    const app = new Hono();
    app.all("/api/realtime", createRealtimeHandler(rt));

    const res = await app.request("http://shop.example/api/realtime", {
      method: "POST",
      headers: { "content-type": "application/json", "x-user": "u2" },
      body: JSON.stringify(batch),
    });
    const refused = await app.request("http://shop.example/api/realtime");

    expect(res.status).toBe(200);
    const body = (await res.json()) as { grants: { token: string }[] };
    expect(readClaims(body.grants[0]?.token ?? "").sub).toBe("u2");
    expect(seen[0]).toBeInstanceOf(Request);
    expect(refused.status).toBe(405);
  });
});
