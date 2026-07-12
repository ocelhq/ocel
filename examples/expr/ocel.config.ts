import { defineConfig } from "@ocel/sdk/config";
import awsProvider from "@ocel/provider-aws";

// Placeholder committed so the example type-checks and reads as complete.
// `ocel init` overwrites this with the real projectId; the e2e harness deletes
// it before running init and restores it afterwards.
export default defineConfig({
  projectId: "expr",
  provider: awsProvider(),
  apps: [{ name: "exp", framework: "express", path: "." }],
});
