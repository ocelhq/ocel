# build-variables/next

A Next app whose dynamic route reads one sensitive and one secret variable at module scope,
so `next build` reads both while it collects page data. An e2e cell asks whether a deploy
builds it, and whether the build leaves either value in `.ocel/output`, in `.next` or in a
live dir.

`app/values/[slug]/route.ts` is the test surface, driven by the suites under
[`tests/e2e`](../../../e2e). It answers each value's sha256, never the value.
