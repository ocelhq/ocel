# client-env/next

A Next app that declares one public plain variable and one sensitive variable through
`ocel/env/next`. An e2e cell asks whether the server, the edge runtime and the browser bundle
read the public value ocel's adapter inlined, whether the page renders the deployment url, and
whether neither the page nor a script it loads holds the sensitive value.

`app/page.tsx`, `app/browser-greeting.tsx` and `app/edge/route.ts` are the test surface, driven
by the suites under [`tests/e2e`](../../../e2e). The page renders the sensitive value's sha256,
never the value.
