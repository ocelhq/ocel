import assert from "node:assert/strict";
import { type ContractRow, OCEL_SVG_BYTES } from "../contract";

export const staticRows: ContractRow[] = [
  {
    title: "GET /ocel.svg serves the svg at its known length",
    run: async (ctx) => {
      const res = await ctx.fetch(`${ctx.baseUrl}/ocel.svg`);
      assert.equal(res.status, 200);
      assert.match(res.headers.get("content-type") ?? "", /^image\/svg\+xml/);
      const bytes = new Uint8Array(await res.arrayBuffer());
      assert.equal(bytes.byteLength, OCEL_SVG_BYTES);
    },
  },
  {
    title: "GET /missing.svg is a 404",
    run: async (ctx) => {
      const res = await ctx.fetch(`${ctx.baseUrl}/missing.svg`);
      assert.equal(res.status, 404);
    },
  },
];
