import assert from "node:assert/strict";
import { type ContractRow, json } from "../contract";
import { linkRows } from "./links";

export const ENV_ROW = "GET /api/probes/env reports the greeting and never the secret";

export const envRows: ContractRow[] = [
  {
    title: ENV_ROW,
    run: async (ctx) => {
      const { res, body } = await json(ctx, "/api/probes/env");
      assert.equal(res.status, 200);
      const probe = body as { greeting: string; hasSecret: boolean; arch: string };
      assert.equal(probe.greeting, ctx.greeting);
      assert.equal(probe.hasSecret, true);
      assert.ok(probe.arch.length > 0);
    },
  },
];

export function setsEnv(rows: ContractRow[]): boolean {
  return rows.some((row) => envRows.includes(row) || linkRows.includes(row));
}
