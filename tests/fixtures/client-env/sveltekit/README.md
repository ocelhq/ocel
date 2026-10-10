# client-env/sveltekit

A SvelteKit 3 app that declares one public plain variable and one sensitive variable through
`ocel/env/sveltekit`, in the discovery folder where ocel looks, re-exported from `src/env.ts` where
SvelteKit looks. An e2e cell asks whether the page renders the public value and the deployment
url, whether the client starts with the public value, and whether neither the page nor a script
it loads holds the sensitive value.

`src/routes/+page.svelte` and `src/routes/+page.server.ts` are the test surface, driven by the
suites under [`tests/e2e`](../../../e2e). The page renders the sensitive value's sha256, never
the value.
