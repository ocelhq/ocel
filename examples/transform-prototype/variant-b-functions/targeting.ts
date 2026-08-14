import { defineTransform } from "./transform"

export default defineTransform([
  {
    if: (ctx) => ctx.app === "api",
    function: { lambda: { memorySizeMb: 2048 } },
  },
  {
    bucket: {
      bucket: (args, ctx) => {
        if (ctx.resourceName.startsWith("assets-")) {
          args.forceDestroy = false
        }
      },
    },
  },
  {
    if: (ctx) => ctx.envClass === "production",
    postgres: { cluster: { deletionProtection: true, skipFinalSnapshot: false } },
  },
  {
    if: (ctx) => ctx.env === "staging",
    tags: { "acme:env": "staging" },
  },
])
