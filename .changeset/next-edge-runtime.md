---
"ocel": minor
---

Serve Next.js edge output — `middleware.ts` and any route with `runtime: 'edge'` — on the Cloudflare edge. The build now emits an edge bundle that a deployment loads at request time, so deploying and rolling back stay pointer flips with no worker script upload. Middleware responses, redirects, request-header overrides and `next.config` `headers()` rules all reach the client, and a middleware that cannot run fails closed with a 500 rather than serving the page it protects. A `middleware.ts` running on the `nodejs` runtime is now a build error naming the file — set `config = { runtime: 'edge' }`.
