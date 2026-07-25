---
"@ocel/sdk": minor
---

Accept `"next"` as an app's `framework` in `ocel.config.ts`. It has always been a first-class framework in the builder, but the public `AppConfig` type only allowed `"express"` and `"fastify"`, so declaring a Next.js app explicitly failed to typecheck.
