import assert from "node:assert/strict";
import { type ContractRow, json } from "../contract";

export const BINDING_ROW =
  "GET /api/binding answers with what it resolved and the greeting it deployed with";
export const BINDING_QUERY_ROW =
  "GET /api/binding/query answers ok after a select through the binding";

export const bindingRows: ContractRow[] = [
  {
    title: BINDING_ROW,
    run: async (ctx) => {
      const { res, body } = await json(ctx, "/api/binding");
      assert.equal(res.status, 200);
      const binding = body as {
        host: string;
        port: number;
        database: string;
        hasPassword: boolean;
        greeting: string;
      };
      assert.ok(binding.host.length > 0, "the app resolved no host");
      assert.equal(typeof binding.port, "number");
      assert.ok(binding.database.length > 0, "the app resolved no database");
      assert.equal(binding.hasPassword, true);
      assert.equal(binding.greeting, ctx.greeting);
    },
  },
  {
    title: BINDING_QUERY_ROW,
    run: async (ctx) => {
      const { res, body } = await json(ctx, "/api/binding/query");
      assert.equal(res.status, 200);
      assert.deepEqual(body, { ok: true });
    },
  },
];
