// The `aws` CLI calls assert-bytecode.mjs and assert-embed.mjs share. Same
// module scripts/e2e-next/aws.mjs re-exports — see
// @ocel-scripts/e2e-shared/aws.mjs for what each helper does.
export * from "@ocel-scripts/e2e-shared/aws.mjs";
