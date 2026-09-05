import assert from "node:assert/strict";
import { type ContractRow, json } from "../contract";

export const LINK_ROW = "GET /api/link answers with what it resolved and the greeting it deployed with";
export const LINK_QUERY_ROW = "GET /api/link/query answers ok after a select through the link";

export const linkRows: ContractRow[] = [
  {
    title: LINK_ROW,
    run: async (ctx) => {
      const { res, body } = await json(ctx, "/api/link");
      assert.equal(res.status, 200);
      const link = body as {
        host: string;
        port: number;
        database: string;
        hasPassword: boolean;
        greeting: string;
      };
      assert.ok(link.host.length > 0, "the app resolved no host");
      assert.equal(typeof link.port, "number");
      assert.ok(link.database.length > 0, "the app resolved no database");
      assert.equal(link.hasPassword, true);
      assert.equal(link.greeting, ctx.greeting);
    },
  },
  {
    title: LINK_QUERY_ROW,
    run: async (ctx) => {
      const { res, body } = await json(ctx, "/api/link/query");
      assert.equal(res.status, 200);
      assert.deepEqual(body, { ok: true });
    },
  },
];
