import assert from "node:assert/strict";
import { type Check, json } from "./context";

export const healthChecks: Check[] = [
  {
    title: "GET /health answers with the app name",
    run: async (ctx) => {
      const { res, body } = await json(ctx, "/health");
      assert.equal(res.status, 200);
      assert.deepEqual(body, { ok: true, app: ctx.app });
    },
  },
];
