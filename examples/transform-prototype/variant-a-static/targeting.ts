import { defineTransform } from "./transform"

export default defineTransform([
  {
    when: { app: "api" },
    function: { lambda: { memorySizeMb: 2048 } },
  },
  {
    when: { name: "assets-*" },
    bucket: { bucket: { forceDestroy: false } },
  },
  {
    when: { envClass: "production" },
    postgres: { cluster: { deletionProtection: true, skipFinalSnapshot: false } },
  },
  {
    when: { envClass: "preview" },
    postgres: { cluster: { minCapacity: 0, maxCapacity: 1 } },
  },
  {
    when: { env: "staging" },
    tags: { "acme:env": "staging" },
  },
  {
    when: { app: ["api", "worker"], envClass: "production" },
    function: { lambda: { timeoutSeconds: 60 } },
  },
])
