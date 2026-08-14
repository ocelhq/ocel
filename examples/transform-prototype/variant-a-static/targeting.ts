import { defineTransform } from "./transform"

export default defineTransform([
  {
    if: (ctx) => ctx.app === "api",
    function: { lambda: { memorySizeMb: 2048 } },
  },
  {
    if: (ctx) => ctx.envClass === "production",
    postgres: { cluster: { deletionProtection: true, skipFinalSnapshot: false } },
  },
  {
    if: (ctx) => ctx.envClass === "preview",
    postgres: { cluster: { minCapacity: 0, maxCapacity: 1 } },
  },
  {
    if: (ctx) => ctx.env === "staging",
    tags: { "acme:env": "staging" },
  },
  {
    if: (ctx) => ["api", "worker"].includes(ctx.app ?? "") && ctx.envClass === "production",
    function: { lambda: { timeoutSeconds: 60 } },
  },
])
