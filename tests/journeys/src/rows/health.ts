import assert from "node:assert/strict";
import { type ContractRow, json } from "../contract";

export const healthRows: ContractRow[] = [
  {
    title: "GET /health answers with the app name",
    run: async (ctx) => {
      const { res, body } = await json(ctx, "/health");
      assert.equal(res.status, 200);
      assert.deepEqual(body, { ok: true, app: ctx.app });
    },
  },
];
