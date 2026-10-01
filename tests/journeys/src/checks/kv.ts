import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { type Check, json } from "./context";

export const kvBindingCheck: Check = {
  title: "GET /api/kv answers with the store it resolved and a password to reach it with",
  run: async (ctx) => {
    const { res, body } = await json(ctx, "/api/kv");
    assert.equal(res.status, 200);
    const binding = body as { host: string; port: number; username: string; hasPassword: boolean };
    assert.ok(binding.host.length > 0, "the app resolved no host");
    assert.ok(Number.isInteger(binding.port) && binding.port > 0, "the app resolved no port");
    assert.ok(binding.username.length > 0, "the app resolved no username");
    assert.equal(binding.hasPassword, true);
  },
};

export const kvPingCheck: Check = {
  title: "GET /api/kv/ping answers PONG through the store's client",
  run: async (ctx) => {
    const { res, body } = await json(ctx, "/api/kv/ping");
    assert.equal(res.status, 200);
    assert.deepEqual(body, { pong: "PONG" });
  },
};

export const kvRoundTripCheck: Check = {
  title: "PUT /api/kv/entries/:key is read back by GET through the store's text entry",
  run: async (ctx) => {
    const key = `round-trip-${randomUUID()}`;
    const value = `journey-${randomUUID()}`;
    const put = await json(ctx, `/api/kv/entries/${key}`, {
      method: "PUT",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ value }),
    });
    assert.equal(put.res.status, 204);
    const { res, body } = await json(ctx, `/api/kv/entries/${key}`);
    assert.equal(res.status, 200);
    assert.deepEqual(body, { value });
  },
};

export const kvMissCheck: Check = {
  title: "GET /api/kv/entries/:key of a key never written is a 404",
  run: async (ctx) => {
    const { res } = await json(ctx, `/api/kv/entries/never-written-${randomUUID()}`);
    assert.equal(res.status, 404);
  },
};

export const kvChecks: Check[] = [kvBindingCheck, kvPingCheck, kvRoundTripCheck, kvMissCheck];
