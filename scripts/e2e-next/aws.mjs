// The `aws` CLI calls assert-bytecode.mjs and assert-embed.mjs share. Moved to
// @ocel-scripts/e2e-shared/aws.mjs so scripts/e2e-node's identical assertion
// scripts read the same code rather than a forked copy — see that module for
// what each helper does and why it lives apart from lib.mjs. Re-exported here
// so every existing `import {...} from "./aws.mjs"` in this directory keeps
// working unchanged.
export * from "@ocel-scripts/e2e-shared/aws.mjs";
